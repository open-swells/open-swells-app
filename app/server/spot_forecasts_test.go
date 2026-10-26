package main

import (
	"compress/gzip"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSpotForecastFixture(t *testing.T, dir, metadataStart, fileStart string, spots map[string]any) {
	t.Helper()
	// Hourly, then the 3-hourly tail, like a real run.
	writeSpotForecastRun(t, dir, metadataStart, fileStart, []int{0, 1, 2, 3, 6}, spots)
}

// writeSpotForecastRun publishes a run like the pipeline does: a new file
// renamed over the old one, which leaves archived links to it intact.
func writeSpotForecastRun(t *testing.T, dir, metadataStart, fileStart string, hours []int, spots map[string]any) {
	t.Helper()
	meta := `{"forecast_start":"` + metadataStart + `"}`
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, spotForecastsFile)
	f, err := os.Create(path + ".tmp")
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	payload := map[string]any{
		"forecast_start": fileStart,
		"hours":          hours,
		"fields":         []string{"hs", "h1", "p1", "d1", "ws", "wd"},
		"spots":          spots,
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
	// A distinct mtime per run, so the loader's cache sees the change.
	if prev, err := os.Stat(path); err == nil {
		mod := prev.ModTime().Add(time.Minute)
		if err := os.Chtimes(f.Name(), mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(f.Name(), path); err != nil {
		t.Fatal(err)
	}
}

// hourlySpot is a spot with n hourly values, height h1 = base + 0.1*i.
func hourlySpot(n int, base float64) map[string]any {
	hs, h1, p1, d1, ws, wd := []any{}, []any{}, []any{}, []any{}, []any{}, []any{}
	for i := 0; i < n; i++ {
		h := math.Round((base+0.1*float64(i))*10) / 10
		hs = append(hs, h+0.5)
		h1 = append(h1, h)
		p1 = append(p1, 12.0)
		d1 = append(d1, 270)
		ws = append(ws, 4.0)
		wd = append(wd, 90)
	}
	return map[string]any{"cell": []float64{0.5, 0}, "km": 55.6,
		"hs": hs, "h1": h1, "p1": p1, "d1": d1, "ws": ws, "wd": wd}
}

func hourRange(n int) []int {
	hours := make([]int, n)
	for i := range hours {
		hours[i] = i
	}
	return hours
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

func TestSpotForecastCarriesWindSea(t *testing.T) {
	dir := t.TempDir()
	spot := fixtureSpot()
	spot["hw"] = []any{0.6, 0.7, nil, 0.8, 1.1}
	spot["pw"] = []any{4.0, 4.0, nil, 4.5, 5.0}
	spot["dw"] = []any{90, 90, nil, 100, 100}
	writeSpotForecastFixture(t, dir, "20260720_00Z", "20260720_00Z", map[string]any{"a": spot})

	forecast, hours := spotForecast(dir, Spot{ID: "a"})
	if got := forecast.Forecast[0].WindSeaHeight; got != "0.6" {
		t.Errorf("hour 0 wind sea = %q, want 0.6", got)
	}
	// A null wind sea hour leaves the row's wind sea empty, never borrowed.
	if got := forecast.Forecast[2].WindSeaHeight; got != "" {
		t.Errorf("hour 2 wind sea = %q, want empty", got)
	}
	// 3 -> 6 interpolates when both ends have it: hour 4 = 0.8 + (1.1-0.8)/3.
	if got := forecast.Forecast[4].WindSeaHeight; got != "0.9" {
		t.Errorf("hour 4 wind sea = %q, want 0.9", got)
	}
	var windSea *SwellComponent
	for i, c := range hours[0].Comps {
		if c.WindSea {
			windSea = &hours[0].Comps[i]
		}
	}
	if windSea == nil || math.Abs(windSea.HeightM-0.6) > 1e-6 || windSea.DirDeg != 90 {
		t.Errorf("classifier comps should include the wind sea: %+v", hours[0].Comps)
	}
}

func TestSpotReportFallsBackToWindSeaWithoutSwell(t *testing.T) {
	row := ForecastRow{WindSeaHeight: "0.8", WindSeaPeriod: "4.4", WindSeaDegrees: "115"}
	row.Time = time.Now().UTC().Truncate(time.Hour)
	report, ok := spotReport(t.TempDir(), Spot{ID: "x"}, ForecastData{Forecast: []ForecastRow{row}})
	if !ok {
		t.Fatal("a wind-sea-only hour should still produce a report")
	}
	if !report.PrimaryIsWindSea || report.PrimaryHeight != "2.6" || report.PrimaryPeriod != "4.4" {
		t.Errorf("report = %+v, want the wind sea as primary", report)
	}
}

func TestSpotForecastBackfillsHoursBeforeRunFromEarlierRuns(t *testing.T) {
	dir := t.TempDir()
	// Runs 18Z and 00Z are seen (and archived) before the 06Z run lands.
	writeSpotForecastRun(t, dir, "20260719_18Z", "20260719_18Z", hourRange(13), map[string]any{"a": hourlySpot(13, 5.0)})
	if _, _, ok := spotSeriesFor(dir, "a"); !ok {
		t.Fatal("18Z run not loaded")
	}
	writeSpotForecastRun(t, dir, "20260720_00Z", "20260720_00Z", hourRange(13), map[string]any{"a": hourlySpot(13, 1.0)})
	if _, _, ok := spotSeriesFor(dir, "a"); !ok {
		t.Fatal("00Z run not loaded")
	}
	writeSpotForecastRun(t, dir, "20260720_06Z", "20260720_06Z", hourRange(4), map[string]any{"a": hourlySpot(4, 3.0)})

	forecast, hours := spotForecast(dir, Spot{ID: "a"})
	// 06Z-12h .. 06Z+3h: 18Z's hours 6..11, 00Z's hours 0..5, then 06Z's own.
	if got := len(forecast.Forecast); got != 16 {
		t.Fatalf("rows = %d, want 16", got)
	}
	if len(hours) != 16 {
		t.Fatalf("classifier hours = %d, want 16", len(hours))
	}
	if got, want := forecast.Forecast[0].Time, time.Date(2026, 7, 19, 18, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("first row at %v, want %v", got, want)
	}
	checks := []struct {
		row  int
		want string
	}{
		{0, "5.0"},  // 18Z f000: no newer run has 18Z
		{5, "5.5"},  // 18Z f005
		{6, "1.0"},  // 00Z f000 beats 18Z f006
		{11, "1.5"}, // 00Z f005
		{12, "3.0"}, // the current run from its start
		{15, "3.3"},
	}
	for _, c := range checks {
		if got := forecast.Forecast[c.row].PrimaryWaveHeight; got != c.want {
			t.Fatalf("row %d primary = %s, want %s", c.row, got, c.want)
		}
	}
	if forecast.Date != "2026072006" {
		t.Fatalf("forecast date = %s, want the current run", forecast.Date)
	}

	// Wind carries the same hours, before the run as negative hours.
	wind := spotWind(dir, Spot{ID: "a"})
	if len(wind) != 16 || wind[0].Hour != -12 || wind[12].Hour != 0 {
		t.Fatalf("wind = %+v", wind)
	}

	// The summary starts at the run's day, not the backfilled day before.
	summary := generateForecastSummary(forecast)
	if len(summary) != 1 || summary[0].Date != "2026-07-20" {
		t.Fatalf("summary = %+v", summary)
	}

	// Archives older than the backfill window are pruned; the rest stay.
	writeSpotForecastRun(t, dir, "20260721_06Z", "20260721_06Z", hourRange(4), map[string]any{"a": hourlySpot(4, 3.0)})
	if _, _, ok := spotSeriesFor(dir, "a"); !ok {
		t.Fatal("next day's run not loaded")
	}
	runs := archivedSpotForecastRuns(dir)
	if _, ok := runs["20260719_18Z"]; ok {
		t.Fatal("18Z archive outlived the backfill window")
	}
	if _, ok := runs["20260720_06Z"]; !ok {
		t.Fatalf("06Z archive pruned too early: %v", runs)
	}
}

func TestSpotForecastBackfillStopsAtGap(t *testing.T) {
	dir := t.TempDir()
	old := hourlySpot(6, 1.0)
	old["hs"].([]any)[3] = nil // 00Z f003 failed
	writeSpotForecastRun(t, dir, "20260720_00Z", "20260720_00Z", hourRange(6), map[string]any{"a": old})
	if _, _, ok := spotSeriesFor(dir, "a"); !ok {
		t.Fatal("00Z run not loaded")
	}
	writeSpotForecastRun(t, dir, "20260720_06Z", "20260720_06Z", hourRange(2), map[string]any{"a": hourlySpot(2, 3.0)})

	forecast, _ := spotForecast(dir, Spot{ID: "a"})
	// Only 00Z f004..f005 join without bridging the failed hour.
	if got := len(forecast.Forecast); got != 4 {
		t.Fatalf("rows = %d, want 4", got)
	}
	if got := forecast.Forecast[0].PrimaryWaveHeight; got != "1.4" {
		t.Fatalf("first row primary = %s, want 00Z f004's 1.4", got)
	}
}

func TestSpotForecastIgnoresBackfillWithoutCurrentRun(t *testing.T) {
	dir := t.TempDir()
	writeSpotForecastRun(t, dir, "20260720_00Z", "20260720_00Z", hourRange(13), map[string]any{"a": hourlySpot(13, 1.0)})
	if _, _, ok := spotSeriesFor(dir, "a"); !ok {
		t.Fatal("00Z run not loaded")
	}
	current := hourlySpot(2, 3.0)
	current["hs"] = []any{nil, nil}
	writeSpotForecastRun(t, dir, "20260720_06Z", "20260720_06Z", hourRange(2), map[string]any{"a": current})

	if forecast, _ := spotForecast(dir, Spot{ID: "a"}); len(forecast.Forecast) != 0 {
		t.Fatalf("rows = %d, want none: only past hours", len(forecast.Forecast))
	}
}
