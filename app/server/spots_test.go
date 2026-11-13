package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSwellGrid publishes a swell bundle on a 2x2 lattice (lat and lon 0
// and 10) whose hours each hold one wet cell, at row-major index cell[hour].
func writeSwellGrid(t *testing.T, dir string, cell map[int]int) {
	t.Helper()
	var hours []int
	values := map[int]map[string][]float64{}
	var vars []bundleVar
	for k := 1; k <= 3; k++ {
		vars = append(vars, bundleVar{fmt.Sprintf("h%d", k), 0.01}, bundleVar{fmt.Sprintf("p%d", k), 0.1}, bundleVar{fmt.Sprintf("d%d", k), 1})
	}
	for hour := 0; hour <= 3*len(cell); hour += 3 {
		c, ok := cell[hour]
		if !ok {
			continue
		}
		hours = append(hours, hour)
		fields := map[string][]float64{}
		for _, v := range vars {
			fields[v.Name] = []float64{math.NaN(), math.NaN(), math.NaN(), math.NaN()}
		}
		fields["h1"][c], fields["p1"][c], fields["d1"][c] = 1.2, 10, 270
		values[hour] = fields
	}
	writeTestBundle(t, filepath.Join(dir, "swell_r.bin"), []float64{0, 10}, []float64{0, 10}, hours, vars, values)
	writeMetadata(t, dir, `{"forecast_start":"20260720_00Z","bundles":{"swell":"swell_r.bin"}}`)
}

func TestSpotForecastKeepsLeadingRunWhenLaterGridMisses(t *testing.T) {
	dir := t.TempDir()
	writeSwellGrid(t, dir, map[int]int{0: 0, 3: 3}) // (0, 0), then only (10, 10)

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
	writeSwellGrid(t, dir, map[int]int{0: 3}) // only (10, 10)

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
