package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSpotForecastKeepsLeadingRunWhenLaterGridMisses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"forecast_start":"20260720_00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	writeGrid := func(hour, coordinates string) {
		t.Helper()
		body := `{"features":[{"geometry":{"coordinates":` + coordinates + `},"properties":{"h1":1.2,"p1":10,"d1":270}}]}`
		if err := os.WriteFile(filepath.Join(dir, "swell_partitions_"+hour+".geojson"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeGrid("000", `[0,0]`)
	writeGrid("003", `[10,10]`)

	forecast, hours := gridSpotForecast(dir, 0, 0)
	if got := len(forecast.Forecast); got != 1 {
		t.Fatalf("forecast rows = %d, want the valid leading row", got)
	}
	if got := len(hours); got != 1 {
		t.Fatalf("swell hours = %d, want the valid leading hour", got)
	}
}

func TestSpotForecastRejectsInitialDistantGrid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"forecast_start":"20260720_00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"features":[{"geometry":{"coordinates":[10,10]},"properties":{"h1":1.2,"p1":10,"d1":270}}]}`
	if err := os.WriteFile(filepath.Join(dir, "swell_partitions_000.geojson"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	forecast, hours := gridSpotForecast(dir, 0, 0)
	if len(forecast.Forecast) != 0 || len(hours) != 0 {
		t.Fatal("forecast should be empty when the first grid has no nearby ocean cell")
	}
}

func TestSpotCoastlineFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spots.json")
	os.WriteFile(path, []byte(`[
		{"id": "auto", "name": "Auto", "lat": 33.6, "lon": -118.0, "facing": 223, "open_water": [[134, 214], [250, 284]]},
		{"id": "tuned", "name": "Tuned", "lat": 33.6, "lon": -118.0, "swell_window": [170, 260], "facing": 223, "open_water": [[134, 214]]}
	]`), 0o644)
	store, err := NewSpotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir() // no forecast files: facing must come from the spot

	auto, _ := store.Get("auto")
	if deg, ok := spotFacing(dir, auto); !ok || deg != 223 {
		t.Errorf("computed facing = %v (ok=%v), want 223", deg, ok)
	}
	b := spotBeach(dir, auto)
	if len(b.OpenWater) != 2 || b.OpenWater[1] != [2]float64{250, 284} || b.Window != nil {
		t.Errorf("auto beach = %+v", b)
	}

	// A hand-tuned window wins: its midpoint is the facing, and open
	// water is not stacked on top of it.
	tuned, _ := store.Get("tuned")
	if deg, _ := spotFacing(dir, tuned); deg != 215 {
		t.Errorf("tuned facing = %v, want the window midpoint 215", deg)
	}
	if b := spotBeach(dir, tuned); b.OpenWater != nil || len(b.Window) != 2 {
		t.Errorf("tuned beach = %+v", b)
	}
}
