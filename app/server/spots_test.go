package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestSpotLocation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lat, lon float64
		zone     string
	}{
		{"California", 33.86, -118.63, "America/Los_Angeles"},
		{"Hawaii", 21.66, -158.05, "Pacific/Honolulu"},
		{"Sydney", -33.89, 151.28, "Australia/Sydney"},
		{"Bali", -8.81, 115.09, "Asia/Makassar"},
		{"Portugal", 38.69, -9.34, "Europe/Lisbon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spot := Spot{Lat: tc.lat, Lon: tc.lon}
			if got := spotLocation(spot).String(); got != tc.zone {
				t.Fatalf("zone = %s, want %s", got, tc.zone)
			}
		})
	}
}

func TestSpotTimezoneOverride(t *testing.T) {
	spot := Spot{ID: "island", Lat: -3.83, Lon: -32.4, TimeZone: "America/Noronha"}
	if got := spotLocation(spot).String(); got != spot.TimeZone {
		t.Fatalf("override = %s, want %s", got, spot.TimeZone)
	}
	spot.TimeZone = "invalid/zone"
	spot.Lat, spot.Lon = 33.86, -118.63
	if got := spotLocation(spot).String(); got != "America/Los_Angeles" {
		t.Fatalf("invalid override fallback = %s", got)
	}
}

func TestIslandTimezoneData(t *testing.T) {
	store, err := NewSpotStore("../data/spots.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, spot := range store.All() {
		if spot.TimeZone != "" {
			if _, err := time.LoadLocation(spot.TimeZone); err != nil {
				t.Errorf("spot %s: %v", spot.ID, err)
			}
		}
	}
	for id, zone := range map[string]string{
		"abras": "America/Noronha", "biboca": "America/Noronha", "boldro": "America/Noronha",
		"cachorro": "America/Noronha", "cacimba-do-padre": "America/Noronha", "conceicao": "America/Noronha", "meio": "America/Noronha",
		"mataveri": "Pacific/Easter", "papa-hanga-roa": "Pacific/Easter", "papa-tangaroa": "Pacific/Easter", "vai-uri": "Pacific/Easter",
		"carola": "Pacific/Galapagos", "el-faro-ecuador-galapagos-islands": "Pacific/Galapagos", "la-loberia": "Pacific/Galapagos", "tongo-reef": "Pacific/Galapagos", "tortuga-bay": "Pacific/Galapagos",
		"tuapeka-point": "Pacific/Chatham", "utua-mamao": "Pacific/Fakaofo", "satans-shank": "Indian/Kerguelen",
	} {
		spot, ok := store.Get(id)
		if !ok {
			t.Fatalf("missing spot %s", id)
		}
		if got := spotLocation(spot).String(); got != zone {
			t.Errorf("spot %s: zone = %s, want %s", id, got, zone)
		}
	}
	appData, err := os.ReadFile("../data/spots.json")
	if err != nil {
		t.Fatal(err)
	}
	pipelineData, err := os.ReadFile("../../pipeline/spots.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(appData, pipelineData) {
		t.Error("app and pipeline spot data must stay synchronized")
	}
}
