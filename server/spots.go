package main

// Surf spots: ~6k named breaks loaded from data/spots.json. Unlike buoys they
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

// --- NWPS nearshore point sampling ----------------------------------------
// NWPS CG1 point grids (nwps_points_<wfo>_<grid>_<HHH>.geojson) carry the
// combined sea state (h/p/d) at ~2-4 km spacing out to 144 h. Where a spot
// falls inside a domain, that combined value becomes the forecast's
// headline number and the global partitions stay as the breakdown.

// H is the combined wind-wave-and-swell height (total sea state, what the
// heatmap shows); S is total swell with wind sea excluded; P/D are the
// dominant component's period and direction (usually the swell).
type nwpsPoint struct {
	Lon, Lat, H, S, P, D float64
}

type nwpsGridEntry struct {
	modTime int64
	points  []nwpsPoint
}

var (
	nwpsGridMu    sync.Mutex
	nwpsGridCache = map[string]nwpsGridEntry{}
)

func nwpsGridPoints(path string) ([]nwpsPoint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	nwpsGridMu.Lock()
	cached, ok := nwpsGridCache[path]
	nwpsGridMu.Unlock()
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
				H float64 `json:"h"`
				S float64 `json:"s"`
				P float64 `json:"p"`
				D float64 `json:"d"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	points := make([]nwpsPoint, 0, len(doc.Features))
	for _, f := range doc.Features {
		points = append(points, nwpsPoint{
			Lon: f.Geometry.Coordinates[0],
			Lat: f.Geometry.Coordinates[1],
			H:   f.Properties.H,
			S:   f.Properties.S,
			P:   f.Properties.P,
			D:   f.Properties.D,
		})
	}

	nwpsGridMu.Lock()
	nwpsGridCache[path] = nwpsGridEntry{modTime: info.ModTime().UnixNano(), points: points}
	nwpsGridMu.Unlock()
	return points, nil
}

// nearestNwps mirrors nearestSwell but with a ~0.1 deg tolerance: NWPS cells
// are a few km apart, so a nearest cell farther than that means the spot is
// outside the domain, not that its cell is offshore.
func nearestNwps(points []nwpsPoint, lat, lon float64) (nwpsPoint, float64, bool) {
	best := nwpsPoint{}
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
	return best, bestDist, bestDist <= 0.01
}

type nwpsDomain struct {
	WFO   string `json:"wfo"`
	Grid  string `json:"grid"`
	Hours []int  `json:"hours"`
}

// nwpsPointDomains reads the nwps_points index from the run's metadata.
func nwpsPointDomains(staticDir string) []nwpsDomain {
	raw, err := os.ReadFile(filepath.Join(staticDir, "metadata.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		NwpsPoints []nwpsDomain `json:"nwps_points"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil
	}
	return meta.NwpsPoints
}

// nwpsSample returns the combined sea state closest to the spot across every
// domain covering this hour (domains can overlap; the nearest cell wins).
func nwpsSample(staticDir string, domains []nwpsDomain, hour int, lat, lon float64) *nwpsPoint {
	var best *nwpsPoint
	bestDist := math.MaxFloat64
	for _, d := range domains {
		covered := false
		for _, h := range d.Hours {
			if h == hour {
				covered = true
				break
			}
		}
		if !covered {
			continue
		}
		path := filepath.Join(staticDir, fmt.Sprintf("nwps_points_%s_%s_%03d.geojson", d.WFO, d.Grid, hour))
		points, err := nwpsGridPoints(path)
		if err != nil {
			continue
		}
		if p, dist, ok := nearestNwps(points, lat, lon); ok && dist < bestDist {
			pCopy := p
			best, bestDist = &pCopy, dist
		}
	}
	return best
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

// lerpNwps blends two nearshore samples; when only the earlier end exists
// (the 144 h NWPS horizon falls mid-interval) its value is held.
func lerpNwps(a, b *nwpsPoint, frac float64) *nwpsPoint {
	if a == nil {
		return nil
	}
	out := *a
	if b != nil {
		out.H = a.H + (b.H-a.H)*frac
		out.S = a.S + (b.S-a.S)*frac
		out.P = a.P + (b.P-a.P)*frac
		out.D = lerpAngle(a.D, b.D, frac)
	}
	return &out
}

// swellRow renders one forecast row. With an NWPS sample the combined
// nearshore sea state is the headline (primary) and the global partitions
// shift down a slot as the breakdown; without one the partitions fill the
// slots from primary as before.
func swellRow(p swellPoint, n *nwpsPoint, t time.Time) ForecastRow {
	row := ForecastRow{
		Date: fmt.Sprintf("%d %d", t.Day(), t.Hour()),
		Time: t,
	}
	slots := []*[3]*string{
		{&row.PrimaryWaveHeight, &row.PrimaryPeriod, &row.PrimaryDegrees},
		{&row.SecondaryWaveHeight, &row.SecondaryPeriod, &row.SecondaryDegrees},
		{&row.TertiaryWaveHeight, &row.TertiaryPeriod, &row.TertiaryDegrees},
		{&row.QuaternaryWaveHeight, &row.QuaternaryPeriod, &row.QuaternaryDegrees},
	}
	next := 0
	if n != nil {
		*slots[0][0] = fmt.Sprintf("%.1f", n.H)
		*slots[0][1] = fmt.Sprintf("%.1f", n.P)
		*slots[0][2] = fmt.Sprintf("%.0f", n.D)
		next = 1
	}
	for i := 0; i < 3 && next < len(slots); i++ {
		if !p.Has[i] {
			continue
		}
		*slots[next][0] = fmt.Sprintf("%.1f", p.H[i])
		*slots[next][1] = fmt.Sprintf("%.1f", p.P[i])
		*slots[next][2] = fmt.Sprintf("%.0f", p.D[i])
		next++
	}
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
		nwps *nwpsPoint
	}
	nwpsDomains := nwpsPointDomains(staticDir)
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
		samples = append(samples, sample{
			hour: hour,
			p:    p,
			nwps: nwpsSample(staticDir, nwpsDomains, hour, lat, lon),
		})
	}
	if len(samples) == 0 {
		return ForecastData{}
	}

	var rows []ForecastRow
	for i := 0; i < len(samples)-1; i++ {
		for sub := 0; sub < 3; sub++ {
			hour := samples[i].hour + sub
			frac := float64(sub) / 3
			p := lerpSwell(samples[i].p, samples[i+1].p, frac)
			n := lerpNwps(samples[i].nwps, samples[i+1].nwps, frac)
			rows = append(rows, swellRow(p, n, start.Add(time.Duration(hour)*time.Hour)))
		}
	}
	last := samples[len(samples)-1]
	rows = append(rows, swellRow(last.p, last.nwps, start.Add(time.Duration(last.hour)*time.Hour)))

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
	// Set when the primary values are NWPS nearshore data: the primary
	// height is then combined seas (wind waves + swell) and SwellHeight
	// is the true swell with wind sea excluded, in feet.
	SwellHeight string
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
		// NWPS swell magnitude at the nearest 3-hourly sample, so the card
		// can pair "seas" (the primary height) with the actual swell.
		hour := int(math.Round(row.Time.Sub(start).Hours()/3)) * 3
		if hour >= 0 {
			if n := nwpsSample(staticDir, nwpsPointDomains(staticDir), hour, lat, lon); n != nil {
				report.SwellHeight = fmt.Sprintf("%.1f", n.S*3.28084)
			}
		}
	}
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
	Zone            SurfZoneForecast
	HasZone         bool
	ForecastSummary []ForecastSummary
}

func spotPageHandler(tmpl *template.Template, staticDir string, zones *SurfZoneStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		spot, ok := spotStore.Get(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "Unknown spot"})
			return
		}
		lat, lon := spot.samplePoint()
		forecastData := spotForecast(staticDir, lat, lon)
		report, hasReport := spotReport(staticDir, lat, lon, forecastData)
		// The NWS surf zone report is matched on the spot itself (not the
		// offshore sample point): zones cover the coastal strip.
		zone, hasZone := zones.ZoneForPoint(spot.Lat, spot.Lon)
		data := SpotPageData{
			ForecastData:    forecastData,
			SwellReport:     SwellReport{StationId: spot.ID},
			SpotName:        spot.Name,
			Region:          spot.Region,
			HasForecast:     len(forecastData.Forecast) > 0,
			Report:          report,
			HasReport:       hasReport,
			Zone:            zone,
			HasZone:         hasZone,
			ForecastSummary: generateForecastSummary(forecastData),
		}
		renderTemplate(c, tmpl, "spot.html", data)
	}
}
