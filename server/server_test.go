package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	webstatic "github.com/open-swells/open-swells-app/web/static"
)

const sampleBull = `  Location : 46221      (33.86N 118.63W)
  Model    : spectral resolution for points
  Cycle    : 20260706 06 UTC

| day &|  Hst  n x |    Hs   Tp  dir |    Hs   Tp  dir |    Hs   Tp  dir |
|  hour|  (m)  - - |   (m)  (s) (deg)|   (m)  (s) (deg)|   (m)  (s) (deg)|
|------+-----------+-----------------+-----------------+-----------------+
|  6 06|  1.05  2  |  0.72 13.5 195  |* 0.75  6.3 274  |                 |
|  6 07|  1.04  3  |  0.71 13.4 196  |  0.74  6.4 275  |  0.20 18.1 210  |
|  1 00|  0.90  1  |  0.60 12.0 200  |                 |                 |
`

func TestParseBullFile(t *testing.T) {
	cycle := time.Date(2026, 7, 6, 6, 0, 0, 0, time.UTC)
	rows, err := parseBullFile(sampleBull, cycle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}

	first := rows[0]
	if first.PrimaryWaveHeight != "0.72" || first.PrimaryPeriod != "13.5" || first.PrimaryDegrees != "195" {
		t.Errorf("bad primary swell: %+v", first)
	}
	if first.SecondaryWaveHeight != "0.75" {
		t.Errorf("starred swell group not parsed: %+v", first)
	}
	if !first.Time.Equal(cycle) {
		t.Errorf("expected first row at cycle time, got %v", first.Time)
	}

	second := rows[1]
	if second.TertiaryWaveHeight != "0.20" || second.TertiaryDegrees != "210" {
		t.Errorf("tertiary swell not parsed: %+v", second)
	}

	// Day 1 after cycle day 6 means the forecast rolled into August.
	rollover := rows[2]
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !rollover.Time.Equal(want) {
		t.Errorf("month rollover: expected %v, got %v", want, rollover.Time)
	}
}

func TestParseBullFileEmpty(t *testing.T) {
	cycle := time.Now().UTC()
	if _, err := parseBullFile("", cycle); err == nil {
		t.Error("expected error for empty bulletin")
	}
	if _, err := parseBullFile("<html>404 not found</html>", cycle); err == nil {
		t.Error("expected error for non-bulletin content")
	}
	if _, err := parseBullFile("| day &| Hst |\n|------+-----+\n", cycle); err == nil {
		t.Error("expected error for header-only bulletin")
	}
}

func TestGenerateForecastSummary(t *testing.T) {
	cycle := time.Date(2026, 7, 6, 6, 0, 0, 0, time.UTC)
	rows, err := parseBullFile(sampleBull, cycle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	summary := generateForecastSummary(ForecastData{Forecast: rows, Date: "2026070606"})
	if len(summary) != 2 {
		t.Fatalf("expected 2 days in summary, got %d: %+v", len(summary), summary)
	}
	if summary[0].Date != "2026-07-06" || summary[1].Date != "2026-08-01" {
		t.Errorf("summary days wrong: %+v", summary)
	}
	if summary[0].Score <= 0 || summary[0].Score > 100 {
		t.Errorf("summary condition score out of range: %+v", summary[0])
	}
	if summary[0].HeightFt <= 0 || summary[0].DayNum != "6" {
		t.Errorf("summary is missing outlook fields: %+v", summary[0])
	}
}

func TestRowTime(t *testing.T) {
	cycle := time.Date(2026, 2, 25, 12, 0, 0, 0, time.UTC)
	got := rowTime(cycle, 3, 6)
	want := time.Date(2026, 3, 3, 6, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("expected %v, got %v", want, got)
	}
	same := rowTime(cycle, 25, 12)
	if !same.Equal(cycle) {
		t.Errorf("expected cycle time back, got %v", same)
	}
}

func TestNDBCValue(t *testing.T) {
	if got := ndbcValue("MM"); got != "" {
		t.Errorf("MM sentinel = %q, want empty", got)
	}
	if got := ndbcValue("  mm "); got != "" {
		t.Errorf("case-insensitive MM sentinel = %q, want empty", got)
	}
	if got := ndbcValue("12.4"); got != "12.4" {
		t.Errorf("reported measurement changed to %q", got)
	}
}

func TestLoadTemplates(t *testing.T) {
	tmpl := loadTemplates(filepath.Join("..", "web", "templates"))
	for _, name := range []string{"landing.html", "about.html", "today.html", "favorites.html", "buoy.html", "report", "forecastsummary"} {
		if tmpl.Lookup(name) == nil {
			t.Errorf("template %q not found", name)
		}
	}
	var favorites bytes.Buffer
	if err := tmpl.ExecuteTemplate(&favorites, "favorites.html", gin.H{"SearchView": false}); err != nil {
		t.Fatalf("favorites template failed to render: %v", err)
	}
	if !bytes.Contains(favorites.Bytes(), []byte("firebasejs/12.16.0/firebase-auth-compat.js")) ||
		!bytes.Contains(favorites.Bytes(), []byte("/assets/firebase-auth.js")) {
		t.Error("favorites page is missing the shared Firebase auth SDK")
	}
	for _, legacy := range [][]byte{[]byte("firebasejs/9."), []byte("firebasejs/11."), []byte("signInWithPopup(new firebase.auth.GoogleAuthProvider())")} {
		if bytes.Contains(favorites.Bytes(), legacy) {
			t.Errorf("favorites page still contains legacy Firebase code %q", legacy)
		}
	}
	var mapPage bytes.Buffer
	if err := tmpl.ExecuteTemplate(&mapPage, "today.html", MapPageData{}); err != nil {
		t.Fatalf("map template failed to render: %v", err)
	}
	if !bytes.Contains(mapPage.Bytes(), []byte("updateUIForSignedInUser(user);")) {
		t.Error("map auth-state listener does not update the account button")
	}
	var landing bytes.Buffer
	if err := tmpl.ExecuteTemplate(&landing, "landing.html", LandingPageData{SpotCount: 5878, BuoyCount: 178}); err != nil {
		t.Fatalf("landing template failed to render: %v", err)
	}
	if !bytes.Contains(landing.Bytes(), []byte("5,878 surf spots")) {
		t.Error("landing template is missing the formatted spot count")
	}
	var unavailableBuoy bytes.Buffer
	if err := tmpl.ExecuteTemplate(&unavailableBuoy, "buoy.html", BuoyPageData{
		BuoyName: "Test Buoy", SwellReport: SwellReport{StationId: "46268"}, HasForecastError: true,
	}); err != nil {
		t.Fatalf("unavailable buoy template failed to render: %v", err)
	}
	if !bytes.Contains(unavailableBuoy.Bytes(), []byte("Forecast not available")) {
		t.Error("unavailable buoy page is missing its forecast status")
	}
	if bytes.Contains(unavailableBuoy.Bytes(), []byte("let data =")) {
		t.Error("unavailable buoy page unexpectedly initialized the forecast chart")
	}
	var spotPage bytes.Buffer
	if err := tmpl.ExecuteTemplate(&spotPage, "spot.html", SpotPageData{
		SpotName: "Test Spot", HasForecast: true, HasReport: true,
		Report: SpotReport{PrimaryHeight: "3.0", PrimaryPeriod: "12", PrimaryDegrees: "225"},
		ForecastData: ForecastData{Date: "2026072000", Forecast: []ForecastRow{{
			Date: "20 00", Time: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC), PrimaryWaveHeight: "0.9",
		}}},
		ForecastSummary: []ForecastSummary{{Condition: "good", WaveHeight: "3.2ft"}},
	}); err != nil {
		t.Fatalf("spot template failed to render: %v", err)
	}
	for _, want := range []string{"Current conditions", "data-current-condition>good", "data-current-height>3.2ft", "--condition-color: #45c4b0"} {
		if !bytes.Contains(spotPage.Bytes(), []byte(want)) {
			t.Errorf("spot condition summary is missing %q", want)
		}
	}
	if bytes.Contains(spotPage.Bytes(), []byte("Current outlook")) {
		t.Error("spot page still renders the detached current outlook tag")
	}
	if !bytes.Contains(spotPage.Bytes(), []byte("travelBearing(d.PrimaryDegrees)")) ||
		!bytes.Contains(spotPage.Bytes(), []byte("travelBearing(direction)")) {
		t.Error("hourly forecast arrows do not convert marine from-directions to travel bearings")
	}
	if bytes.Contains(spotPage.Bytes(), []byte("PrimaryDegrees - 135")) {
		t.Error("hourly forecast still applies the incorrect swell-arrow rotation offset")
	}
	summaryData := struct {
		Buoys    []BuoyWithSummary
		Spots    []SpotFavorite
		Detailed bool
	}{
		Buoys: []BuoyWithSummary{{
			Buoy: Buoy{ID: "46221", Name: "Santa Monica Bay", Forecast: ForecastData{Forecast: []ForecastRow{{
				Time: time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC), PrimaryWaveHeight: "1.25",
			}}}},
			Summary: []ForecastSummary{
				{DateAbv: "Mon 7/20", DayNum: "20", WaveHeight: "3.2ft", HeightFt: 3.2, Condition: "good", Score: 57},
				{DateAbv: "Tue 7/21", DayNum: "21", WaveHeight: "4.1ft", HeightFt: 4.1, Condition: "good", Score: 63},
			},
		}},
		Spots: []SpotFavorite{{
			ID: "el-porto", Name: "El Porto", Region: "Los Angeles", WindSpeed: "12", WindDir: "270", HasWind: true,
			Summary:           []ForecastSummary{{DateAbv: "Mon 7/20", DayNum: "20", WaveHeight: "3.2ft", HeightFt: 3.2, Condition: "fair", Score: 40}},
			CurrentConditions: []ConditionCandle{{Hour: "06", UnixMs: 1753000000000, Score: 40, Condition: "fair"}},
		}},
		Detailed: true,
	}
	var detailed bytes.Buffer
	if err := tmpl.ExecuteTemplate(&detailed, "forecastsummary", summaryData); err != nil {
		t.Fatalf("forecast summary template failed to render: %v", err)
	}
	for _, want := range []string{"data-hour-strip", "data-buoy-outlook", "data-hourly-heights=\"1.25 ", "day-tick", "12 mph", "270&deg;", "data-favorite-condition-card", "Current conditions", "fair", "3.2ft"} {
		if !bytes.Contains(detailed.Bytes(), []byte(want)) {
			t.Errorf("detailed forecast summary is missing %q", want)
		}
	}
	summaryData.Detailed = false
	var compact bytes.Buffer
	if err := tmpl.ExecuteTemplate(&compact, "forecastsummary", summaryData); err != nil {
		t.Fatalf("compact forecast summary template failed to render: %v", err)
	}
	if bytes.Contains(compact.Bytes(), []byte("data-swell-line")) || bytes.Contains(compact.Bytes(), []byte("data-hour-strip")) {
		t.Error("compact forecast summary unexpectedly rendered detailed-only sections")
	}
	if !bytes.Contains(compact.Bytes(), []byte("Show on map")) {
		t.Error("compact forecast summary is missing its map action")
	}
	for _, want := range []string{"href=\"/forecast/46221\"", "href=\"/spot/el-porto\"", "Open full forecast"} {
		if !bytes.Contains(compact.Bytes(), []byte(want)) {
			t.Errorf("compact forecast summary is missing %q", want)
		}
	}
	if !bytes.Contains(compact.Bytes(), []byte("data-favorite-condition-card")) {
		t.Error("compact forecast summary is missing the spot condition card")
	}

	unavailableSummary := summaryData
	unavailableSummary.Buoys = []BuoyWithSummary{{
		Buoy:        Buoy{ID: "46268", Name: "Test Buoy"},
		SwellReport: SwellReport{PrimaryWaveHeight: "1.2", PrimaryPeriod: "12"},
		HasError:    true, ErrorMsg: "Forecast not available",
	}}
	unavailableSummary.Detailed = true
	var unavailableFavorite bytes.Buffer
	if err := tmpl.ExecuteTemplate(&unavailableFavorite, "forecastsummary", unavailableSummary); err != nil {
		t.Fatalf("unavailable favorite summary failed to render: %v", err)
	}
	for _, want := range []string{"1.2ft @ 12s", "Forecast not available", "Current buoy observations are still shown."} {
		if !bytes.Contains(unavailableFavorite.Bytes(), []byte(want)) {
			t.Errorf("unavailable favorite summary is missing %q", want)
		}
	}
}

func TestEmbeddedFirebaseAuthAsset(t *testing.T) {
	if len(webstatic.FirebaseAuthJS) == 0 {
		t.Fatal("embedded Firebase auth asset is empty")
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/assets/firebase-auth.js", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", webstatic.FirebaseAuthJS)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/assets/firebase-auth.js", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/javascript; charset=utf-8" {
		t.Fatalf("asset Content-Type = %q", got)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("window.openSwellsAuth")) {
		t.Fatal("asset response is missing the Firebase auth bootstrap")
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := []byte(`
# local config
OPEN_SWELLS_TEST_SINGLE='/tmp/firebase.json'
OPEN_SWELLS_TEST_DOUBLE="./main.db"
export OPEN_SWELLS_TEST_EXPORT=./data/forecast
OPEN_SWELLS_TEST_EXISTING=from-file
MALFORMED_LINE
`)
	if err := os.WriteFile(envPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	keys := []string{
		"OPEN_SWELLS_TEST_SINGLE",
		"OPEN_SWELLS_TEST_DOUBLE",
		"OPEN_SWELLS_TEST_EXPORT",
		"OPEN_SWELLS_TEST_EXISTING",
	}
	for _, key := range keys {
		os.Unsetenv(key)
		t.Cleanup(func() { os.Unsetenv(key) })
	}
	os.Setenv("OPEN_SWELLS_TEST_EXISTING", "from-env")

	if err := loadDotEnv(envPath); err != nil {
		t.Fatalf("loadDotEnv returned error: %v", err)
	}

	if got := os.Getenv("OPEN_SWELLS_TEST_SINGLE"); got != "/tmp/firebase.json" {
		t.Errorf("single quoted value = %q", got)
	}
	if got := os.Getenv("OPEN_SWELLS_TEST_DOUBLE"); got != "./main.db" {
		t.Errorf("double quoted value = %q", got)
	}
	if got := os.Getenv("OPEN_SWELLS_TEST_EXPORT"); got != "./data/forecast" {
		t.Errorf("export value = %q", got)
	}
	if got := os.Getenv("OPEN_SWELLS_TEST_EXISTING"); got != "from-env" {
		t.Errorf("existing env was overwritten: %q", got)
	}
}

func TestStaticHandlerServesPrecompressed(t *testing.T) {
	dir := t.TempDir()
	plain := []byte(`{"type":"FeatureCollection","features":[]}`)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(plain)
	zw.Close()
	os.WriteFile(filepath.Join(dir, "contours_000.geojson"), plain, 0o644)
	os.WriteFile(filepath.Join(dir, "contours_000.geojson.gz"), gz.Bytes(), 0o644)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/static/*filepath", staticHandler(dir))

	// gzip-capable client gets the precompressed file
	req := httptest.NewRequest("GET", "/static/contours_000.geojson", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("expected gzip response, got code=%d enc=%q", w.Code, w.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(w.Body.Bytes(), gz.Bytes()) {
		t.Error("gzip body mismatch")
	}

	// client without gzip support gets the plain file
	req = httptest.NewRequest("GET", "/static/contours_000.geojson", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "" {
		t.Errorf("expected plain response, got code=%d enc=%q", w.Code, w.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(w.Body.Bytes(), plain) {
		t.Error("plain body mismatch")
	}

	// a geojson without a shipped .gz sibling gets compressed on demand
	plain2 := []byte(`{"type":"FeatureCollection","features":[],"note":"no sibling"}`)
	os.WriteFile(filepath.Join(dir, "contours_001.geojson"), plain2, 0o644)
	req = httptest.NewRequest("GET", "/static/contours_001.geojson", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected on-demand gzip, got code=%d enc=%q", w.Code, w.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("bad gzip body: %v", err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, plain2) {
		t.Errorf("on-demand gzip roundtrip mismatch: %q", got)
	}
	// and the sibling is cached on disk for the next request
	if _, err := os.Stat(filepath.Join(dir, "contours_001.geojson.gz")); err != nil {
		t.Errorf("gz sibling not cached: %v", err)
	}

	// a stale .gz (older than its source) is rebuilt, not served
	stale := filepath.Join(dir, "contours_002.geojson")
	staleGz := stale + ".gz"
	os.WriteFile(staleGz, gz.Bytes(), 0o644) // gzip of the old contours_000 body
	past := time.Now().Add(-time.Hour)
	os.Chtimes(staleGz, past, past)
	fresh := []byte(`{"type":"FeatureCollection","features":[],"note":"fresh"}`)
	os.WriteFile(stale, fresh, 0o644)
	req = httptest.NewRequest("GET", "/static/contours_002.geojson", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	zr, err = gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("bad gzip body for rebuilt sibling: %v", err)
	}
	got, _ = io.ReadAll(zr)
	if !bytes.Equal(got, fresh) {
		t.Errorf("stale gz served instead of rebuilt: %q", got)
	}

	// path traversal stays inside the static dir
	req = httptest.NewRequest("GET", "/static/../main.go", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == 200 && bytes.Contains(w.Body.Bytes(), []byte("package main")) {
		t.Error("path traversal escaped static dir")
	}
}
