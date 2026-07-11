package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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

func TestLoadTemplates(t *testing.T) {
	tmpl := loadTemplates()
	for _, name := range []string{"landing.html", "about.html", "today.html", "buoy.html", "report", "forecastsummary"} {
		if tmpl.Lookup(name) == nil {
			t.Errorf("template %q not found", name)
		}
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := []byte(`
# local config
OPEN_SWELLS_TEST_SINGLE='/tmp/firebase.json'
OPEN_SWELLS_TEST_DOUBLE="./main.db"
export OPEN_SWELLS_TEST_EXPORT=./static
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
	if got := os.Getenv("OPEN_SWELLS_TEST_EXPORT"); got != "./static" {
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
