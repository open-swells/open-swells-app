package main

// Surf spots: ~6k named breaks loaded from beaches.json. Unlike buoys they
// have no station bulletin, so a spot's forecast is sampled from the
// swell_partitions_XXX.geojson grids the pipeline writes into the static
// dir — nearest wet grid point per forecast hour, interpolated to hourly
// rows so the buoy forecast template renders them unchanged.
//
// The spot list is static per deploy, so /api/spots serves bytes marshaled
// (and gzipped) once at startup.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var spotStore *SpotStore

type Spot struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Region string  `json:"region,omitempty"`
	// Optional hand-tuned point to sample the wave grid at, for spots
	// where the nearest grid cell to the beach itself picks badly
	// (deep bays, island shadows).
	SampleLat *float64 `json:"sample_lat,omitempty"`
	SampleLon *float64 `json:"sample_lon,omitempty"`
}

// samplePoint is where this spot reads the wave/wind grids.
func (s Spot) samplePoint() (lat, lon float64) {
	if s.SampleLat != nil && s.SampleLon != nil {
		return *s.SampleLat, *s.SampleLon
	}
	return s.Lat, s.Lon
}

type SpotStore struct {
	byID     map[string]Spot
	listJSON []byte // pre-marshaled /api/spots payload
	listGzip []byte
}

func NewSpotStore(path string) (*SpotStore, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spots []Spot
	if err := json.Unmarshal(raw, &spots); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	byID := make(map[string]Spot, len(spots))
	list := make([]gin.H, 0, len(spots))
	for _, s := range spots {
		if s.ID == "" || s.Name == "" {
			continue
		}
		byID[s.ID] = s
		list = append(list, gin.H{
			"id": s.ID, "name": s.Name, "lat": s.Lat, "lon": s.Lon, "region": s.Region,
		})
	}
	listJSON, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(listJSON); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return &SpotStore{byID: byID, listJSON: listJSON, listGzip: buf.Bytes()}, nil
}

func (s *SpotStore) Get(id string) (Spot, bool) {
	if s == nil {
		return Spot{}, false
	}
	sp, ok := s.byID[id]
	return sp, ok
}

func (s *SpotStore) handleList(c *gin.Context) {
	if s == nil {
		c.JSON(http.StatusOK, []gin.H{})
		return
	}
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Vary", "Accept-Encoding")
	if strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
		c.Header("Content-Encoding", "gzip")
		c.Data(http.StatusOK, "application/json", s.listGzip)
		return
	}
	c.Data(http.StatusOK, "application/json", s.listJSON)
}

// --- Swell grid sampling --------------------------------------------------

type swellPoint struct {
	Lon, Lat float64
	H, P, D  [3]float64
	Has      [3]bool
}

type swellGridEntry struct {
	modTime int64
	points  []swellPoint
}

var (
	swellGridMu    sync.Mutex
	swellGridCache = map[string]swellGridEntry{}
)

func swellGridPoints(path string) ([]swellPoint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	swellGridMu.Lock()
	cached, ok := swellGridCache[path]
	swellGridMu.Unlock()
	if ok && cached.modTime == info.ModTime().UnixNano() {
		return cached.points, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Features []struct {
			Geometry struct {
				Coordinates [2]float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties struct {
				H1 *float64 `json:"h1"`
				P1 *float64 `json:"p1"`
				D1 *float64 `json:"d1"`
				H2 *float64 `json:"h2"`
				P2 *float64 `json:"p2"`
				D2 *float64 `json:"d2"`
				H3 *float64 `json:"h3"`
				P3 *float64 `json:"p3"`
				D3 *float64 `json:"d3"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	points := make([]swellPoint, 0, len(doc.Features))
	for _, f := range doc.Features {
		pt := swellPoint{
			Lon: f.Geometry.Coordinates[0],
			Lat: f.Geometry.Coordinates[1],
		}
		parts := [3][3]*float64{
			{f.Properties.H1, f.Properties.P1, f.Properties.D1},
			{f.Properties.H2, f.Properties.P2, f.Properties.D2},
			{f.Properties.H3, f.Properties.P3, f.Properties.D3},
		}
		for i, p := range parts {
			if p[0] != nil && p[1] != nil && p[2] != nil {
				pt.H[i], pt.P[i], pt.D[i] = *p[0], *p[1], *p[2]
				pt.Has[i] = true
			}
		}
		points = append(points, pt)
	}

	swellGridMu.Lock()
	swellGridCache[path] = swellGridEntry{modTime: info.ModTime().UnixNano(), points: points}
	swellGridMu.Unlock()
	return points, nil
}

// nearestSwell picks the grid point closest to (lat, lon), tolerant of the
// grid using 0..360 longitudes while spots use -180..180. The partitions
// grid is ~1.67 deg spaced, so anything beyond ~3 deg is not this spot's
// water.
func nearestSwell(points []swellPoint, lat, lon float64) (swellPoint, bool) {
	best := swellPoint{}
	bestDist := math.MaxFloat64
	for _, p := range points {
		dlng := math.Abs(p.Lon - lon)
		if dlng > 180 {
			dlng = 360 - dlng
		}
		dist := dlng*dlng + (p.Lat-lat)*(p.Lat-lat)
		if dist < bestDist {
			bestDist = dist
			best = p
		}
	}
	return best, bestDist <= 9
}

// lerpAngle interpolates degrees along the shortest arc.
func lerpAngle(a, b, frac float64) float64 {
	diff := math.Mod(b-a+540, 360) - 180
	return math.Mod(a+diff*frac+360, 360)
}

// lerpSwell blends two grid samples. A partition is interpolated only when
// both ends have it; otherwise the earlier sample's values are held so a
// system fading out doesn't blend with an unrelated one.
func lerpSwell(a, b swellPoint, frac float64) swellPoint {
	out := a
	for i := 0; i < 3; i++ {
		if a.Has[i] && b.Has[i] {
			out.H[i] = a.H[i] + (b.H[i]-a.H[i])*frac
			out.P[i] = a.P[i] + (b.P[i]-a.P[i])*frac
			out.D[i] = lerpAngle(a.D[i], b.D[i], frac)
		}
	}
	return out
}

func swellRow(p swellPoint, t time.Time) ForecastRow {
	row := ForecastRow{
		Date: fmt.Sprintf("%d %d", t.Day(), t.Hour()),
		Time: t,
	}
	set := func(h, per, d *string, i int) {
		if p.Has[i] {
			*h = fmt.Sprintf("%.1f", p.H[i])
			*per = fmt.Sprintf("%.1f", p.P[i])
			*d = fmt.Sprintf("%.0f", p.D[i])
		}
	}
	set(&row.PrimaryWaveHeight, &row.PrimaryPeriod, &row.PrimaryDegrees, 0)
	set(&row.SecondaryWaveHeight, &row.SecondaryPeriod, &row.SecondaryDegrees, 1)
	set(&row.TertiaryWaveHeight, &row.TertiaryPeriod, &row.TertiaryDegrees, 2)
	return row
}

// spotForecast builds a buoy-bulletin-shaped forecast for a location by
// sampling every 3-hourly partitions grid and interpolating to hourly rows
// (the forecast chart assumes consecutive hourly rows, like bulletins).
// Only the leading gap-free run of hours is used: rows after a missing
// file would land on the wrong timeline slot.
func spotForecast(staticDir string, lat, lon float64) ForecastData {
	start, err := time.Parse("20060102_15Z", windForecastStart(staticDir))
	if err != nil {
		return ForecastData{}
	}

	type sample struct {
		hour int
		p    swellPoint
	}
	var samples []sample
	for hour := 0; hour <= windMaxHour; hour += 3 {
		path := filepath.Join(staticDir, fmt.Sprintf("swell_partitions_%03d.geojson", hour))
		points, err := swellGridPoints(path)
		if err != nil {
			break // hour not generated — end of this run's horizon
		}
		p, ok := nearestSwell(points, lat, lon)
		if !ok {
			return ForecastData{} // no wave grid near this spot at all
		}
		if len(samples) > 0 && hour != samples[len(samples)-1].hour+3 {
			break
		}
		samples = append(samples, sample{hour: hour, p: p})
	}
	if len(samples) == 0 {
		return ForecastData{}
	}

	var rows []ForecastRow
	for i := 0; i < len(samples)-1; i++ {
		for sub := 0; sub < 3; sub++ {
			hour := samples[i].hour + sub
			p := lerpSwell(samples[i].p, samples[i+1].p, float64(sub)/3)
			rows = append(rows, swellRow(p, start.Add(time.Duration(hour)*time.Hour)))
		}
	}
	last := samples[len(samples)-1]
	rows = append(rows, swellRow(last.p, start.Add(time.Duration(last.hour)*time.Hour)))

	return ForecastData{Forecast: rows, Date: start.Format("2006010215")}
}

// --- Favorites drawer entries ---------------------------------------------

// SpotFavorite is one favorite-spot card in the forecast summary drawer.
// Swell strings are pre-formatted from the first (current-hour) model row,
// since spots have no live observations to show.
type SpotFavorite struct {
	ID, Name, Region        string
	Summary                 []ForecastSummary
	Primary, PrimarySub     string
	Secondary, SecondarySub string
	HasError                bool
}

func spotFavoriteEntry(staticDir string, spot Spot) SpotFavorite {
	entry := SpotFavorite{ID: spot.ID, Name: spot.Name, Region: spot.Region}
	lat, lon := spot.samplePoint()
	forecastData := spotForecast(staticDir, lat, lon)
	if len(forecastData.Forecast) == 0 {
		entry.HasError = true
		return entry
	}
	entry.Summary = generateForecastSummary(forecastData)

	row := forecastData.Forecast[0]
	format := func(h, p, d string) (string, string) {
		meters, err := strconv.ParseFloat(h, 64)
		if err != nil {
			return "", ""
		}
		return fmt.Sprintf("%.1fft @ %ss", meters*3.28084, p), d
	}
	entry.Primary, entry.PrimarySub = format(row.PrimaryWaveHeight, row.PrimaryPeriod, row.PrimaryDegrees)
	entry.Secondary, entry.SecondarySub = format(row.SecondaryWaveHeight, row.SecondaryPeriod, row.SecondaryDegrees)
	return entry
}

// --- Spot forecast page ---------------------------------------------------

// SpotReport is the spot page's "current report": the model forecast valid
// closest to now, standing in for the live observations a buoy would have.
// Heights are pre-converted to feet to match the buoy report cards.
type SpotReport struct {
	Valid                                              string // e.g. "Jul 17 03:00 UTC"
	PrimaryHeight, PrimaryPeriod, PrimaryDegrees       string
	SecondaryHeight, SecondaryPeriod, SecondaryDegrees string
	HasSecondary                                       bool
	WindSpeed, WindDir                                 string // mph, degrees true
	HasWind                                            bool
}

// spotReport picks the forecast row whose valid time is closest to now
// (rows are hourly, so there is almost always one on the current hour) and
// the wind sample likewise.
func spotReport(staticDir string, lat, lon float64, forecastData ForecastData) (SpotReport, bool) {
	now := time.Now().UTC()
	var report SpotReport

	best := -1
	var bestDiff time.Duration
	for i, row := range forecastData.Forecast {
		diff := row.Time.Sub(now)
		if diff < 0 {
			diff = -diff
		}
		if best == -1 || diff < bestDiff {
			best, bestDiff = i, diff
		}
	}
	if best == -1 || bestDiff > 6*time.Hour {
		return report, false
	}
	row := forecastData.Forecast[best]
	report.Valid = row.Time.Format("Jan 2 15:04 UTC")

	toFeet := func(h string) string {
		meters, err := strconv.ParseFloat(h, 64)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%.1f", meters*3.28084)
	}
	report.PrimaryHeight = toFeet(row.PrimaryWaveHeight)
	report.PrimaryPeriod = row.PrimaryPeriod
	report.PrimaryDegrees = row.PrimaryDegrees
	report.SecondaryHeight = toFeet(row.SecondaryWaveHeight)
	report.SecondaryPeriod = row.SecondaryPeriod
	report.SecondaryDegrees = row.SecondaryDegrees
	report.HasSecondary = report.SecondaryHeight != ""
	if report.PrimaryHeight == "" {
		return report, false
	}

	start, err := time.Parse("20060102_15Z", windForecastStart(staticDir))
	if err == nil {
		bestWind := -1
		var bestWindDiff time.Duration
		samples := windSeriesFor(staticDir, lat, lon)
		for i, s := range samples {
			diff := start.Add(time.Duration(s.Hour) * time.Hour).Sub(now)
			if diff < 0 {
				diff = -diff
			}
			if bestWind == -1 || diff < bestWindDiff {
				bestWind, bestWindDiff = i, diff
			}
		}
		if bestWind != -1 && bestWindDiff <= 6*time.Hour {
			report.WindSpeed = fmt.Sprintf("%.0f", samples[bestWind].Speed*2.23694)
			report.WindDir = fmt.Sprintf("%.0f", samples[bestWind].Dir)
			report.HasWind = true
		}
	}
	return report, true
}

type SpotPageData struct {
	ForecastData    ForecastData
	SwellReport     SwellReport // StationId feeds the template's wind fetch
	SpotName        string
	Region          string
	HasForecast     bool
	Report          SpotReport
	HasReport       bool
	ForecastSummary []ForecastSummary
}

func spotPageHandler(tmpl *template.Template, staticDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		spot, ok := spotStore.Get(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "Unknown spot"})
			return
		}
		lat, lon := spot.samplePoint()
		forecastData := spotForecast(staticDir, lat, lon)
		report, hasReport := spotReport(staticDir, lat, lon, forecastData)
		data := SpotPageData{
			ForecastData:    forecastData,
			SwellReport:     SwellReport{StationId: spot.ID},
			SpotName:        spot.Name,
			Region:          spot.Region,
			HasForecast:     len(forecastData.Forecast) > 0,
			Report:          report,
			HasReport:       hasReport,
			ForecastSummary: generateForecastSummary(forecastData),
		}
		renderTemplate(c, tmpl, "spot.html", data)
	}
}
