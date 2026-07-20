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

	forecast, hours := spotForecast(dir, 0, 0)
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

	forecast, hours := spotForecast(dir, 0, 0)
	if len(forecast.Forecast) != 0 || len(hours) != 0 {
		t.Fatal("forecast should be empty when the first grid has no nearby ocean cell")
	}
}
