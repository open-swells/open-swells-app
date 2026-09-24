package main

import (
	"compress/gzip"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writeSpotForecastFixture(t *testing.T, dir, metadataStart, fileStart string, spots map[string]any) {
	t.Helper()
	meta := `{"forecast_start":"` + metadataStart + `"}`
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, spotForecastsFile))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	payload := map[string]any{
		"forecast_start": fileStart,
		// Hourly, then the 3-hourly tail, like a real run.
		"hours":  []int{0, 1, 2, 3, 6},
		"fields": []string{"hs", "h1", "p1", "d1", "ws", "wd"},
		"spots":  spots,
	}
	if err := json.NewEncoder(zw).Encode(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func fixtureSpot() map[string]any {
	return map[string]any{
		"cell": []float64{0.5, 0},
		"km":   55.6,
		"hs":   []any{1.5, 1.6, 1.7, 1.8, 2.4},
		"h1":   []any{1.0, 1.1, 1.2, 1.3, 1.9},
		"p1":   []any{12.0, 12.0, 12.0, 12.0, 15.0},
		"d1":   []any{270, 270, 270, 270, 270},
		"ws":   []any{3.0, nil, 5.0, 6.0, 9.0},
		"wd":   []any{90, nil, 90, 90, 90},
	}
}

func TestFloatSeriesDecodesNullsAsNaN(t *testing.T) {
	var s floatSeries
	if err := json.Unmarshal([]byte(`[1.25, null ,-3,0]`), &s); err != nil {
		t.Fatal(err)
	}
	if len(s) != 4 || s[0] != 1.25 || !math.IsNaN(float64(s[1])) || s[2] != -3 || s[3] != 0 {
		t.Fatalf("decoded %v", s)
	}
	if err := json.Unmarshal([]byte(`[]`), &s); err != nil || len(s) != 0 {
		t.Fatalf("empty array: %v %v", s, err)
	}
	if err := json.Unmarshal([]byte(`[1,"x"]`), &s); err == nil {
		t.Fatal("expected an error for a non-number element")
	}
	if !math.IsNaN(s.at(10)) {
		t.Fatal("out-of-range index should read as NaN")
	}
}

func TestSpotForecastUsesFileSeriesHourly(t *testing.T) {
	dir := t.TempDir()
	writeSpotForecastFixture(t, dir, "20260720_00Z", "20260720_00Z", map[string]any{"a": fixtureSpot()})

	forecast, hours := spotForecast(dir, Spot{ID: "a"})
	// Hours 0..6: four hourly samples, then 3->6 interpolated to hourly.
	if got := len(forecast.Forecast); got != 7 {
		t.Fatalf("rows = %d, want 7", got)
	}
	if len(hours) != 7 {
		t.Fatalf("classifier hours = %d, want 7", len(hours))
	}
	if got := forecast.Forecast[1].PrimaryWaveHeight; got != "1.1" {
		t.Fatalf("hour 1 primary = %s, want the file's hourly 1.1", got)
	}
	if got := forecast.Forecast[4].PrimaryWaveHeight; got != "1.5" {
		t.Fatalf("hour 4 primary = %s, want 1.5 interpolated between 1.3 and 1.9", got)
	}
	if got := forecast.Forecast[6].PrimaryPeriod; got != "15.0" {
		t.Fatalf("hour 6 period = %s, want 15.0", got)
	}
	if forecast.Date != "2026072000" {
		t.Fatalf("forecast date = %s", forecast.Date)
	}
}

func TestSpotForecastStopsAtFailedHour(t *testing.T) {
	dir := t.TempDir()
	spot := fixtureSpot()
	spot["hs"] = []any{1.5, 1.6, nil, 1.8, 2.4}
	writeSpotForecastFixture(t, dir, "20260720_00Z", "20260720_00Z", map[string]any{"a": spot})

	forecast, _ := spotForecast(dir, Spot{ID: "a"})
	if got := len(forecast.Forecast); got != 2 {
		t.Fatalf("rows = %d, want the 2-hour leading run before the failed hour", got)
	}
}

func TestSpotForecastIgnoresStaleFile(t *testing.T) {
	dir := t.TempDir()
	writeSpotForecastFixture(t, dir, "20260720_06Z", "20260720_00Z", map[string]any{"a": fixtureSpot()})

	if _, _, ok := spotSeriesFor(dir, "a"); ok {
		t.Fatal("a file from an older run than metadata.json must not be used")
	}
	// No coarse grids in dir either, so the fallback finds nothing.
	if forecast, _ := spotForecast(dir, Spot{ID: "a"}); len(forecast.Forecast) != 0 {
		t.Fatal("stale file leaked into the forecast")
	}
}

func TestSpotForecastFallsBackToGridsForUnknownSpot(t *testing.T) {
	dir := t.TempDir()
	writeSpotForecastFixture(t, dir, "20260720_00Z", "20260720_00Z", map[string]any{"a": fixtureSpot()})
	grid := `{"features":[{"geometry":{"coordinates":[0,0]},"properties":{"h1":0.7,"p1":9,"d1":200}}]}`
	for _, hour := range []string{"000", "003"} {
		if err := os.WriteFile(filepath.Join(dir, "swell_partitions_"+hour+".geojson"), []byte(grid), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	forecast, _ := spotForecast(dir, Spot{ID: "not-in-file"})
	if got := len(forecast.Forecast); got != 4 {
		t.Fatalf("rows = %d, want 4 from the 3-hourly grid fallback", got)
	}
	if got := forecast.Forecast[0].PrimaryWaveHeight; got != "0.7" {
		t.Fatalf("primary = %s, want the grid's 0.7", got)
	}
}

func TestSpotWindUsesFileAndSkipsMissingHours(t *testing.T) {
	dir := t.TempDir()
	writeSpotForecastFixture(t, dir, "20260720_00Z", "20260720_00Z", map[string]any{"a": fixtureSpot()})

	samples := spotWind(dir, Spot{ID: "a"})
	wantHours := []int{0, 2, 3, 6}
	if len(samples) != len(wantHours) {
		t.Fatalf("samples = %+v", samples)
	}
	for i, s := range samples {
		if s.Hour != wantHours[i] || s.Dir != 90 {
			t.Fatalf("sample %d = %+v", i, s)
		}
	}
}

func TestSpotFacingUsesFileCell(t *testing.T) {
	dir := t.TempDir()
	writeSpotForecastFixture(t, dir, "20260720_00Z", "20260720_00Z", map[string]any{"a": fixtureSpot()})

	// The fixture cell is half a degree due north of a spot at 0,0.
	deg, ok := spotFacing(dir, Spot{ID: "a"})
	if !ok || math.Abs(deg) > 1e-9 {
		t.Fatalf("facing = %v %v, want 0 (north)", deg, ok)
	}

	// A cell on top of the spot carries no direction; with no coarse grid
	// to fall back on the facing stays unknown.
	close := fixtureSpot()
	close["cell"] = []float64{0.01, 0}
	other := t.TempDir()
	writeSpotForecastFixture(t, other, "20260720_00Z", "20260720_00Z", map[string]any{"a": close})
	if _, ok := spotFacing(other, Spot{ID: "a"}); ok {
		t.Fatal("expected no facing from a cell on top of the spot")
	}
}
