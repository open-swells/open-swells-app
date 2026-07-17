package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"github.com/gin-gonic/gin"
	"github.com/go-resty/resty/v2"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/sync/singleflight"
	"google.golang.org/api/option"
)

type ForecastRow struct {
	Date                 string
	Time                 time.Time
	PrimaryWaveHeight    string
	PrimaryPeriod        string
	PrimaryDegrees       string
	SecondaryWaveHeight  string
	SecondaryPeriod      string
	SecondaryDegrees     string
	TertiaryWaveHeight   string
	TertiaryPeriod       string
	TertiaryDegrees      string
	QuaternaryWaveHeight string
	QuaternaryPeriod     string
	QuaternaryDegrees    string
}

type ForecastData struct {
	Forecast []ForecastRow
	Date     string
}

type Buoy struct {
	ID       string
	Name     string
	Forecast ForecastData
}

type ReportData struct {
	WindReport  WindReport
	SwellReport SwellReport
}

type MapPageData struct {
	ForecastData ForecastData
	WindReport   WindReport
	SwellReport  SwellReport
}

type BuoyPageData struct {
	ForecastData    ForecastData
	SwellReport     SwellReport
	WindReport      WindReport
	BuoyName        string
	HasSwellError   bool
	HasWindError    bool
	ForecastSummary []ForecastSummary
}

type ForecastSummary struct {
	Date       string
	DateAbv    string
	Condition  string
	WaveHeight string
}

type SwellReport struct {
	StationId           string
	Date                string
	PrimaryWaveHeight   string
	PrimaryPeriod       string
	PrimaryDegrees      string
	SecondaryWaveHeight string
	SecondaryPeriod     string
	SecondaryDegrees    string
	Steepness           string
}

type WindReport struct {
	StationId string
	Date      string
	WindSpeed string
	WindGust  string
	WindDir   string
	AirTemp   string
	WaterTemp string
}

type CacheItem struct {
	Data      interface{}
	Timestamp time.Time
}

type Cache struct {
	items map[string]CacheItem
	ttl   time.Duration
	mutex sync.RWMutex
}

func NewCache(ttl time.Duration) *Cache {
	c := &Cache{
		items: make(map[string]CacheItem),
		ttl:   ttl,
	}
	go c.purgeLoop()
	return c
}

func (c *Cache) Set(key string, value interface{}) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.items[key] = CacheItem{
		Data:      value,
		Timestamp: time.Now(),
	}
}

func (c *Cache) Get(key string) (interface{}, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	item, found := c.items[key]
	if !found || time.Since(item.Timestamp) > c.ttl {
		return nil, false
	}
	return item.Data, true
}

func (c *Cache) purgeLoop() {
	ticker := time.NewTicker(c.ttl)
	for range ticker.C {
		c.mutex.Lock()
		for key, item := range c.items {
			if time.Since(item.Timestamp) > c.ttl {
				delete(c.items, key)
			}
		}
		c.mutex.Unlock()
	}
}

var (
	authClient    *auth.Client
	httpClient    = resty.New().SetTimeout(15 * time.Second)
	ndbcClient    = &http.Client{Timeout: 15 * time.Second}
	forecastGroup singleflight.Group
	stationIDRe   = regexp.MustCompile(`^[A-Za-z0-9]{3,10}$`)
)

const nomadsBase = "https://nomads.ncep.noaa.gov/pub/data/nccf/com/gfs/prod"

// rowTime resolves a bulletin "day hour" pair against the model cycle time.
// Bulletins only carry day-of-month, so a day earlier than the cycle's means
// the forecast has rolled into the next month.
func rowTime(cycle time.Time, day, hour int) time.Time {
	t := time.Date(cycle.Year(), cycle.Month(), day, hour, 0, 0, 0, time.UTC)
	if t.Before(cycle) {
		t = t.AddDate(0, 1, 0)
	}
	return t
}

// parseBullFile parses a GFS wave station bulletin. Data rows are
// |-delimited: | day hour | Hst n x | hs tp dir | hs tp dir | ...
// Header and separator rows are skipped because their first cell is not
// a "day hour" number pair.
func parseBullFile(data string, cycle time.Time) ([]ForecastRow, error) {
	if strings.TrimSpace(data) == "" {
		return nil, errors.New("empty bulletin")
	}
	var forecast []ForecastRow
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		timeParts := strings.Fields(cells[0])
		if len(timeParts) != 2 {
			continue
		}
		day, err1 := strconv.Atoi(timeParts[0])
		hour, err2 := strconv.Atoi(timeParts[1])
		if err1 != nil || err2 != nil {
			continue
		}

		row := ForecastRow{
			Date: timeParts[0] + " " + timeParts[1],
			Time: rowTime(cycle, day, hour),
		}
		var swells [][3]string
		for _, cell := range cells[2:] {
			fields := strings.Fields(strings.ReplaceAll(cell, "*", ""))
			if len(fields) < 3 {
				continue
			}
			swells = append(swells, [3]string{fields[0], fields[1], fields[2]})
			if len(swells) == 4 {
				break
			}
		}
		if len(swells) == 0 {
			continue
		}
		for i, s := range swells {
			switch i {
			case 0:
				row.PrimaryWaveHeight, row.PrimaryPeriod, row.PrimaryDegrees = s[0], s[1], s[2]
			case 1:
				row.SecondaryWaveHeight, row.SecondaryPeriod, row.SecondaryDegrees = s[0], s[1], s[2]
			case 2:
				row.TertiaryWaveHeight, row.TertiaryPeriod, row.TertiaryDegrees = s[0], s[1], s[2]
			case 3:
				row.QuaternaryWaveHeight, row.QuaternaryPeriod, row.QuaternaryDegrees = s[0], s[1], s[2]
			}
		}
		forecast = append(forecast, row)
	}
	if len(forecast) == 0 {
		return nil, errors.New("no forecast rows found in bulletin")
	}
	return forecast, nil
}

func getSwellReport(stationId string) (SwellReport, error) {
	if stationId == "" {
		return SwellReport{}, nil
	}
	resp, err := httpClient.R().Get("https://www.ndbc.noaa.gov/data/realtime2/" + stationId + ".spec")
	if err != nil {
		return SwellReport{}, err
	}
	if resp.StatusCode() != http.StatusOK {
		return SwellReport{}, fmt.Errorf("failed to fetch data: HTTP status %d", resp.StatusCode())
	}

	lines := strings.Split(resp.String(), "\n")
	if len(lines) < 3 {
		return SwellReport{}, fmt.Errorf("insufficient data in response")
	}
	parts := strings.Fields(lines[2])
	if len(parts) < 15 {
		return SwellReport{}, fmt.Errorf("insufficient data in response line")
	}

	secondaryDegrees := parts[10]
	if degrees, ok := directionMap[secondaryDegrees]; ok {
		secondaryDegrees = fmt.Sprintf("%.0f", degrees)
	}

	report := SwellReport{
		StationId:           stationId,
		Date:                parts[0] + "/" + parts[1] + "/" + parts[2] + " " + parts[3] + ":" + parts[4],
		PrimaryWaveHeight:   parts[5],
		PrimaryPeriod:       parts[7],
		PrimaryDegrees:      parts[14],
		SecondaryWaveHeight: parts[8],
		SecondaryPeriod:     parts[9],
		SecondaryDegrees:    secondaryDegrees,
		Steepness:           parts[12],
	}
	return report, nil
}

func getWindReport(stationId string) (WindReport, error) {
	if stationId == "" {
		return WindReport{}, nil
	}
	resp, err := httpClient.R().Get("https://www.ndbc.noaa.gov/data/realtime2/" + stationId + ".txt")
	if err != nil {
		return WindReport{}, err
	}
	if resp.StatusCode() != http.StatusOK {
		return WindReport{}, fmt.Errorf("failed to fetch data: HTTP status %d", resp.StatusCode())
	}
	lines := strings.Split(resp.String(), "\n")
	if len(lines) < 3 {
		return WindReport{}, fmt.Errorf("insufficient data in response")
	}
	parts := strings.Fields(lines[2])
	if len(parts) < 15 {
		return WindReport{}, fmt.Errorf("insufficient data in response line")
	}
	report := WindReport{
		StationId: stationId,
		Date:      parts[0] + " " + parts[1] + " " + parts[2] + " " + parts[3],
		WindSpeed: parts[6],
		WindGust:  parts[7],
		WindDir:   parts[5],
		AirTemp:   parts[13],
		WaterTemp: parts[14],
	}
	return report, nil
}

// fetchForecast tries the most recent model cycles, newest first, and returns
// the first bulletin that both downloads and parses.
func fetchForecast(stationId string) (ForecastData, error) {
	cycle := time.Now().UTC().Truncate(6 * time.Hour)
	var lastErr error
	for i := 0; i < 4; i++ {
		t := cycle.Add(time.Duration(-6*i) * time.Hour)
		url := fmt.Sprintf("%s/gfs.%s/%02d/wave/station/bulls.t%02dz/gfswave.%s.bull",
			nomadsBase, t.Format("20060102"), t.Hour(), t.Hour(), stationId)

		resp, err := httpClient.R().Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode() != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d fetching %s", resp.StatusCode(), url)
			continue
		}
		rows, err := parseBullFile(resp.String(), t)
		if err != nil {
			lastErr = fmt.Errorf("parsing bulletin from %s: %w", url, err)
			continue
		}
		return ForecastData{Forecast: rows, Date: t.Format("2006010215")}, nil
	}
	return ForecastData{}, fmt.Errorf("no usable bulletin for station %s: %w", stationId, lastErr)
}

func getForecast(cache *Cache, stationId string) (ForecastData, error) {
	if cached, found := cache.Get(stationId); found {
		if data, ok := cached.(ForecastData); ok {
			return data, nil
		}
	}
	v, err, _ := forecastGroup.Do(stationId, func() (interface{}, error) {
		data, err := fetchForecast(stationId)
		if err != nil {
			return nil, err
		}
		cache.Set(stationId, data)
		return data, nil
	})
	if err != nil {
		return ForecastData{}, err
	}
	return v.(ForecastData), nil
}

func calculateAverageWaveHeight(forecast []ForecastRow) float64 {
	total := 0.0
	count := 0
	for _, row := range forecast {
		height, err := strconv.ParseFloat(row.PrimaryWaveHeight, 64)
		if err == nil {
			total += height
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func determineCondition(avgWaveHeight float64) string {
	heightInFeet := avgWaveHeight * 3.28084
	if heightInFeet < 0.5 {
		return "poor"
	} else if heightInFeet < 1.5 {
		return "fair"
	}
	return "good"
}

func formatDate(date string) string {
	parsedDate, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return parsedDate.Format("Mon 1/2")
}

func generateForecastSummary(forecastData ForecastData) []ForecastSummary {
	groupedForecast := make(map[string][]ForecastRow)
	for _, row := range forecastData.Forecast {
		day := row.Time.Format("2006-01-02")
		groupedForecast[day] = append(groupedForecast[day], row)
	}

	var summary []ForecastSummary
	for day, rows := range groupedForecast {
		avgWaveHeight := calculateAverageWaveHeight(rows)
		summary = append(summary, ForecastSummary{
			Date:       day,
			DateAbv:    formatDate(day),
			Condition:  determineCondition(avgWaveHeight),
			WaveHeight: fmt.Sprintf("%.1fft", avgWaveHeight*3.28084),
		})
	}

	sort.Slice(summary, func(i, j int) bool {
		return summary[i].Date < summary[j].Date
	})
	return summary
}

type BuoyWithSummary struct {
	Buoy
	Summary      []ForecastSummary
	SwellReport  SwellReport
	WindReport   WindReport
	HasError     bool
	ErrorMsg     string
	Offline      bool
	OfflineSince string // e.g. "Jul 3"; only set when Offline
}

func renderForecastSummary(w http.ResponseWriter, tmpl *template.Template, cache *Cache, uid string, db *sql.DB, staticDir string) {
	buoyIDs, err := getBuoysForUser(db, uid)
	if err != nil {
		log.Printf("Failed to get buoys for user: %v", err)
		http.Error(w, "failed to load favorites", http.StatusInternalServerError)
		return
	}
	spotIDs, err := getSpotsForUser(db, uid)
	if err != nil {
		log.Printf("Failed to get spots for user: %v", err)
		http.Error(w, "failed to load favorites", http.StatusInternalServerError)
		return
	}
	spots := make([]SpotFavorite, 0, len(spotIDs))
	for _, spotID := range spotIDs {
		if spot, ok := spotStore.Get(spotID); ok {
			spots = append(spots, spotFavoriteEntry(staticDir, spot))
		}
	}

	buoys := make([]BuoyWithSummary, len(buoyIDs))
	var wg sync.WaitGroup
	for i, buoyID := range buoyIDs {
		wg.Add(1)
		go func(i int, buoyID string) {
			defer wg.Done()

			entry := BuoyWithSummary{
				Buoy: Buoy{ID: buoyID, Name: stationStore.DisplayName(buoyID)},
			}
			if since, offline := stationStore.OfflineSince(buoyID); offline {
				entry.Offline = true
				entry.OfflineSince = since.Format("Jan 2")
			}
			if swellReport, reportErr := getSwellReport(buoyID); reportErr != nil {
				log.Printf("Error fetching swell report for buoy %s: %v", buoyID, reportErr)
			} else {
				entry.SwellReport = swellReport
			}
			if windReport, reportErr := getWindReport(buoyID); reportErr != nil {
				log.Printf("Error fetching wind report for buoy %s: %v", buoyID, reportErr)
			} else {
				entry.WindReport = windReport
			}

			forecastData, err := getForecast(cache, buoyID)
			if err != nil {
				log.Printf("Error fetching data for buoy %s: %v", buoyID, err)
				entry.HasError = true
				entry.ErrorMsg = "Data unavailable"
			} else {
				entry.Summary = generateForecastSummary(forecastData)
				entry.Buoy.Forecast = forecastData
			}
			buoys[i] = entry
		}(i, buoyID)
	}
	wg.Wait()

	err = tmpl.ExecuteTemplate(w, "forecastsummary", struct {
		Buoys []BuoyWithSummary
		Spots []SpotFavorite
	}{Buoys: buoys, Spots: spots})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func openDatabase(path string) *sql.DB {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS users (uid TEXT PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS user_buoys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid TEXT NOT NULL,
			buoy_id TEXT NOT NULL,
			FOREIGN KEY (uid) REFERENCES users (uid) ON DELETE CASCADE
		)`,
		// Dedupe before the unique index so the migration succeeds on
		// databases populated before the constraint existed.
		`DELETE FROM user_buoys WHERE id NOT IN (
			SELECT MIN(id) FROM user_buoys GROUP BY uid, buoy_id
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_user_buoys_uid_buoy ON user_buoys (uid, buoy_id)`,
		// Favorite surf spots (ids from beaches.json), same shape as
		// user_buoys.
		`CREATE TABLE IF NOT EXISTS user_spots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid TEXT NOT NULL,
			spot_id TEXT NOT NULL,
			FOREIGN KEY (uid) REFERENCES users (uid) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_user_spots_uid_spot ON user_spots (uid, spot_id)`,
		// Station registry, refreshed daily from NDBC (see stations.go).
		// Inactive rows are stations that stopped reporting; they are kept
		// so favorites keep resolving to a name.
		`CREATE TABLE IF NOT EXISTS buoys (
			station_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			latitude REAL NOT NULL,
			longitude REAL NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			last_seen TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		// Surf zone geometry from api.weather.gov, fetched once per zone
		// (see surfzone.go). Zone boundaries are static in practice.
		`CREATE TABLE IF NOT EXISTS surf_zones (
			zone_id TEXT PRIMARY KEY,
			latitude REAL NOT NULL,
			longitude REAL NOT NULL,
			geometry TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			log.Fatalf("Failed to run migration: %v", err)
		}
	}
	return db
}

func insertUserBuoy(db *sql.DB, uid, buoyID string) error {
	if _, err := db.Exec(`INSERT OR IGNORE INTO users (uid) VALUES (?)`, uid); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO user_buoys (uid, buoy_id) VALUES (?, ?)`, uid, buoyID)
	return err
}

func deleteUserBuoy(db *sql.DB, uid, buoyID string) error {
	_, err := db.Exec(`DELETE FROM user_buoys WHERE uid = ? AND buoy_id = ?`, uid, buoyID)
	return err
}

func getBuoysForUser(db *sql.DB, uid string) ([]string, error) {
	rows, err := db.Query(`SELECT buoy_id FROM user_buoys WHERE uid = ?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buoys := []string{}
	for rows.Next() {
		var buoyID string
		if err := rows.Scan(&buoyID); err != nil {
			return nil, err
		}
		buoys = append(buoys, buoyID)
	}
	return buoys, rows.Err()
}

func insertUserSpot(db *sql.DB, uid, spotID string) error {
	if _, err := db.Exec(`INSERT OR IGNORE INTO users (uid) VALUES (?)`, uid); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO user_spots (uid, spot_id) VALUES (?, ?)`, uid, spotID)
	return err
}

func deleteUserSpot(db *sql.DB, uid, spotID string) error {
	_, err := db.Exec(`DELETE FROM user_spots WHERE uid = ? AND spot_id = ?`, uid, spotID)
	return err
}

func getSpotsForUser(db *sql.DB, uid string) ([]string, error) {
	rows, err := db.Query(`SELECT spot_id FROM user_spots WHERE uid = ?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	spots := []string{}
	for rows.Next() {
		var spotID string
		if err := rows.Scan(&spotID); err != nil {
			return nil, err
		}
		spots = append(spots, spotID)
	}
	return spots, rows.Err()
}

// requireAuth verifies the Firebase ID token from the Authorization header
// and stores the caller's UID in the context. The UID must always come from
// the verified token, never from request parameters.
func requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		const prefix = "Bearer "
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, prefix) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Missing bearer token"})
			return
		}
		token, err := authClient.VerifyIDToken(c.Request.Context(), strings.TrimPrefix(header, prefix))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid ID token"})
			return
		}
		c.Set("uid", token.UID)
		c.Next()
	}
}

func validBuoyID(id string) bool {
	return stationStore.Has(id)
}

func loadTemplates() *template.Template {
	tmpl := template.New("").Funcs(template.FuncMap{
		"json":     jsonForTemplate,
		"ripcolor": ripRiskColor,
		// NWS remark fields are often a literal "None." — not worth a row
		"notnone": func(s string) bool {
			t := strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "."))
			return t != "" && t != "none"
		},
	})
	template.Must(tmpl.ParseGlob("templates/*.html"))
	template.Must(tmpl.ParseGlob("pages/*.html"))
	return tmpl
}

// ripRiskColor mirrors RIP_COLORS in the map page: green/amber/red keyed on
// the first word of the NWS rip current risk phrase.
func ripRiskColor(risk string) template.CSS {
	fields := strings.Fields(strings.ToLower(risk))
	if len(fields) > 0 {
		switch fields[0] {
		case "low":
			return "#6fcf97"
		case "moderate":
			return "#e2b86b"
		case "high":
			return "#e06c75"
		}
	}
	return "#8b8d93"
}

func jsonForTemplate(v interface{}) (template.JS, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return template.JS(data), nil
}

func renderTemplate(c *gin.Context, tmpl *template.Template, name string, data interface{}) {
	htmlBuffer := new(bytes.Buffer)
	if err := tmpl.ExecuteTemplate(htmlBuffer, name, data); err != nil {
		log.Println(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, htmlBuffer.String())
}

var gzipGroup singleflight.Group

// ensureGzipSibling returns the path to an up-to-date .gz sibling of full,
// compressing it on first request when the pipeline didn't ship one. A .gz
// older than its source is treated as stale and rebuilt. singleflight keeps
// concurrent requests from compressing the same file twice.
func ensureGzipSibling(full string) (string, error) {
	gzPath := full + ".gz"
	src, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if gz, err := os.Stat(gzPath); err == nil && !gz.ModTime().Before(src.ModTime()) {
		return gzPath, nil
	}

	_, err, _ = gzipGroup.Do(gzPath, func() (interface{}, error) {
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		tmp, err := os.CreateTemp(filepath.Dir(full), ".gz-tmp-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp.Name())

		zw := gzip.NewWriter(tmp)
		if _, err := zw.Write(data); err != nil {
			tmp.Close()
			return nil, err
		}
		if err := zw.Close(); err != nil {
			tmp.Close()
			return nil, err
		}
		if err := tmp.Close(); err != nil {
			return nil, err
		}
		// Match the source mtime so the freshness check above keeps passing.
		if err := os.Chtimes(tmp.Name(), src.ModTime(), src.ModTime()); err != nil {
			return nil, err
		}
		return nil, os.Rename(tmp.Name(), gzPath)
	})
	if err != nil {
		return "", err
	}
	return gzPath, nil
}

// staticHandler serves the static dir with cache headers. For geojson it
// serves a precompressed .gz sibling to gzip-capable clients, generating
// (and caching) the sibling on demand when the pipeline didn't ship one.
func staticHandler(staticDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		clean := path.Clean("/" + c.Param("filepath"))
		full := filepath.Join(staticDir, filepath.FromSlash(clean))

		if strings.HasSuffix(clean, "metadata.json") {
			c.Header("Cache-Control", "public, max-age=60")
		} else {
			c.Header("Cache-Control", "public, max-age=1800")
		}

		// tides.json is ~100KB of hourly series; worth compressing like the
		// geojson layers.
		if (strings.HasSuffix(clean, ".geojson") || strings.HasSuffix(clean, "tides.json")) &&
			strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			if gzPath, err := ensureGzipSibling(full); err == nil {
				c.Header("Content-Encoding", "gzip")
				if strings.HasSuffix(clean, ".geojson") {
					c.Header("Content-Type", "application/geo+json")
				} else {
					c.Header("Content-Type", "application/json")
				}
				c.Header("Vary", "Accept-Encoding")
				c.File(gzPath)
				return
			}
		}
		c.File(full)
	}
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadDotEnv(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}

		if len(value) >= 2 {
			first := value[0]
			last := value[len(value)-1]
			if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
				value = value[1 : len(value)-1]
			}
		}

		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func healthHandler(db *sql.DB, staticDir string, surfStore *SurfZoneStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		problems := []string{}
		warnings := []string{}
		if err := db.Ping(); err != nil {
			problems = append(problems, "database unreachable")
		}

		// Beach forecasts degrade the page, not the service: an NWS outage
		// with zones still cached is a warning; no zones at all (and a
		// failing refresh) means the beach layer is actually down.
		if surfStore != nil {
			if err, zones := surfStore.LastError(), surfStore.ZoneCount(); err != nil {
				if zones == 0 {
					problems = append(problems, fmt.Sprintf("surf zone data unavailable: %v", err))
				} else {
					warnings = append(warnings, fmt.Sprintf("surf zone refresh: %v", err))
				}
			}
		}

		metadataPath := filepath.Join(staticDir, "metadata.json")
		if raw, err := os.ReadFile(metadataPath); err != nil {
			problems = append(problems, "metadata.json unreadable")
		} else {
			var meta struct {
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal(raw, &meta) != nil {
				problems = append(problems, "metadata.json malformed")
			} else if ts, err := time.Parse(time.RFC3339, meta.Timestamp); err != nil {
				problems = append(problems, "metadata timestamp malformed")
			} else if time.Since(ts) > 24*time.Hour {
				problems = append(problems, fmt.Sprintf("contour data stale (%.0fh old)", time.Since(ts).Hours()))
			}
		}

		if len(problems) > 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "problems": problems, "warnings": warnings})
			return
		}
		resp := gin.H{"status": "ok"}
		if len(warnings) > 0 {
			resp["warnings"] = warnings
		}
		c.JSON(http.StatusOK, resp)
	}
}

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("warning: failed to load .env: %v", err)
	}

	ctx := context.Background()

	var opts []option.ClientOption
	if credPath := os.Getenv("FIREBASE_CREDENTIALS"); credPath != "" {
		opts = append(opts, option.WithCredentialsFile(credPath))
	}
	app, err := firebase.NewApp(ctx, nil, opts...)
	if err != nil {
		log.Fatalf("error initializing firebase app (set FIREBASE_CREDENTIALS to the service account key path): %v", err)
	}
	authClient, err = app.Auth(ctx)
	if err != nil {
		log.Fatalf("error getting firebase auth client: %v", err)
	}

	dbPath := getenvDefault("DB_PATH", "./main.db")
	staticDir := getenvDefault("STATIC_DIR", "./static")
	port := getenvDefault("PORT", "8081")

	db := openDatabase(dbPath)
	defer db.Close()

	stationStore = NewStationStore(db)
	if err := stationStore.Load(); err != nil {
		log.Fatalf("failed to load station list: %v", err)
	}
	go stationStore.RunRefresher()

	spotStore, err = NewSpotStore(getenvDefault("SPOTS_PATH", "./beaches.json"))
	if err != nil {
		// The map degrades to no spot layer; everything else still works.
		log.Printf("warning: failed to load surf spots: %v", err)
	}

	surfZoneStore := NewSurfZoneStore(db, strings.Split(getenvDefault("SURF_ZONE_STATES", "ca"), ","))
	if err := surfZoneStore.LoadGeometry(); err != nil {
		log.Printf("warning: failed to load surf zone geometry cache: %v", err)
	}
	go surfZoneStore.RunRefresher()

	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	cache := NewCache(3 * time.Hour)
	tmpl := loadTemplates()

	router.ForwardedByClientIP = true
	trustedProxies := strings.Split(getenvDefault("TRUSTED_PROXIES", "127.0.0.1,::1"), ",")
	if err := router.SetTrustedProxies(trustedProxies); err != nil {
		log.Fatalf("failed to set proxies: %v", err)
	}

	router.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/static/*filepath", staticHandler(staticDir))
	router.GET("/healthz", healthHandler(db, staticDir, surfZoneStore))
	router.GET("/api/buoys", stationStore.handleList)
	router.GET("/api/wind/:stationId", windForecastHandler(staticDir))
	router.GET("/api/beaches", surfZoneStore.handleList)
	router.GET("/api/beach/:zoneId", surfZoneStore.handleZone)
	router.GET("/api/spots", spotStore.handleList)
	router.GET("/spot/:id", spotPageHandler(tmpl, staticDir, surfZoneStore))

	router.GET("/", func(c *gin.Context) {
		renderTemplate(c, tmpl, "landing.html", nil)
	})

	router.GET("/about", func(c *gin.Context) {
		renderTemplate(c, tmpl, "about.html", nil)
	})

	router.GET("/map", func(c *gin.Context) {
		// A NOMADS outage should degrade the map page, not 500 it.
		forecastdata, err := getForecast(cache, "46221")
		if err != nil {
			log.Printf("Warning: failed to get default forecast for map: %v", err)
			forecastdata = ForecastData{}
		}
		renderTemplate(c, tmpl, "today.html", MapPageData{ForecastData: forecastdata})
	})

	// Full-page beach forecast, shown standalone or inside the map's modal
	// iframe (same pattern as /forecast/:stationId).
	router.GET("/beach/:zoneId", func(c *gin.Context) {
		zoneID := strings.ToUpper(c.Param("zoneId"))
		if !surfZoneIDRe.MatchString(zoneID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Unknown zone"})
			return
		}
		f, ok := surfZoneStore.Get(zoneID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "Unknown zone"})
			return
		}
		renderTemplate(c, tmpl, "beach.html", f)
	})

	router.GET("/forecast/:stationId", func(c *gin.Context) {
		stationId := c.Param("stationId")
		if !validBuoyID(stationId) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Unknown station"})
			return
		}

		swellreport, swellErr := getSwellReport(stationId)
		if swellErr != nil {
			log.Printf("Warning: failed to get swell report for buoy %s: %v", stationId, swellErr)
			swellreport = SwellReport{StationId: stationId}
		}

		windreport, windErr := getWindReport(stationId)
		if windErr != nil {
			log.Printf("Warning: failed to get wind report for buoy %s: %v", stationId, windErr)
			windreport = WindReport{StationId: stationId}
		}

		forecastdata, forecastErr := getForecast(cache, stationId)
		if forecastErr != nil {
			log.Printf("Error: failed to get forecast data for buoy %s: %v", stationId, forecastErr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get forecast data"})
			return
		}

		returndata := BuoyPageData{
			ForecastData:    forecastdata,
			SwellReport:     swellreport,
			WindReport:      windreport,
			BuoyName:        stationStore.DisplayName(stationId),
			HasSwellError:   swellErr != nil,
			HasWindError:    windErr != nil,
			ForecastSummary: generateForecastSummary(forecastdata),
		}
		renderTemplate(c, tmpl, "buoy.html", returndata)
	})

	reportHandler := func(c *gin.Context, templateName string) {
		stationId := c.Param("stationId")
		if !stationIDRe.MatchString(stationId) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid station ID"})
			return
		}
		windreport, err := getWindReport(stationId)
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get wind report"})
			return
		}
		swellreport, err := getSwellReport(stationId)
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get swell report"})
			return
		}
		renderTemplate(c, tmpl, templateName, ReportData{WindReport: windreport, SwellReport: swellreport})
	}

	router.GET("/report/:stationId", func(c *gin.Context) {
		reportHandler(c, "report")
	})

	router.GET("/forecast-summary", requireAuth(), func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		renderForecastSummary(c.Writer, tmpl, cache, c.GetString("uid"), db, staticDir)
	})

	realtimeHandler := func(c *gin.Context, extension string) {
		stationId := c.Param("stationId")
		if !stationIDRe.MatchString(stationId) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid station ID"})
			return
		}
		resp, err := ndbcClient.Get("https://www.ndbc.noaa.gov/data/realtime2/" + stationId + extension)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch data"})
			return
		}
		defer resp.Body.Close()

		c.Header("Content-Type", resp.Header.Get("Content-Type"))
		c.Status(resp.StatusCode)
		io.Copy(c.Writer, resp.Body)
	}

	api := router.Group("/api")
	api.GET("/realtime/:stationId", func(c *gin.Context) {
		realtimeHandler(c, ".spec")
	})
	api.GET("/realtime/wind/:stationId", func(c *gin.Context) {
		realtimeHandler(c, ".txt")
	})

	api.POST("/auth", func(c *gin.Context) {
		var req struct {
			IDToken string `json:"idToken"`
		}
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
			return
		}

		token, err := authClient.VerifyIDToken(c.Request.Context(), req.IDToken)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid ID token"})
			return
		}

		user, err := authClient.GetUser(c.Request.Context(), token.UID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error fetching user"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"name":  user.DisplayName,
			"email": user.Email,
		})
	})

	favorites := api.Group("/favorites", requireAuth())
	favorites.GET("", func(c *gin.Context) {
		uid := c.GetString("uid")
		buoys, err := getBuoysForUser(db, uid)
		if err != nil {
			log.Printf("Failed to get user buoys: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user buoys"})
			return
		}
		spots, err := getSpotsForUser(db, uid)
		if err != nil {
			log.Printf("Failed to get user spots: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get user spots"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"buoys": buoys, "spots": spots})
	})
	favorites.POST("/:buoyID", func(c *gin.Context) {
		buoyID := c.Param("buoyID")
		if !validBuoyID(buoyID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown buoy ID"})
			return
		}
		if err := insertUserBuoy(db, c.GetString("uid"), buoyID); err != nil {
			log.Printf("Failed to insert user buoy: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add favorite"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Favorite added"})
	})
	favorites.DELETE("/:buoyID", func(c *gin.Context) {
		if err := deleteUserBuoy(db, c.GetString("uid"), c.Param("buoyID")); err != nil {
			log.Printf("Failed to delete user buoy: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove favorite"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Favorite removed"})
	})

	// Spot favorites live in their own group: gin can't mix a static
	// "spots" segment with the ":buoyID" wildcard above.
	spotFavorites := api.Group("/spot-favorites", requireAuth())
	spotFavorites.POST("/:spotID", func(c *gin.Context) {
		spotID := c.Param("spotID")
		if _, ok := spotStore.Get(spotID); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown spot ID"})
			return
		}
		if err := insertUserSpot(db, c.GetString("uid"), spotID); err != nil {
			log.Printf("Failed to insert user spot: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add favorite"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Favorite added"})
	})
	spotFavorites.DELETE("/:spotID", func(c *gin.Context) {
		if err := deleteUserSpot(db, c.GetString("uid"), c.Param("spotID")); err != nil {
			log.Printf("Failed to delete user spot: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove favorite"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Favorite removed"})
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	shutdownCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-shutdownCtx.Done()

	log.Println("shutting down...")
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(timeoutCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
