package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWarmForecastCachesOnce(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"metadata.json":                   `{"nwps_points":[{"wfo":"lox","grid":"cg1","hours":[0]}]}`,
		"swell_partitions_000.geojson":    `{"features":[{"geometry":{"coordinates":[-118,34]},"properties":{"h1":1,"p1":10,"d1":270}}]}`,
		"wind_000.geojson":                `{"features":[{"geometry":{"coordinates":[-118,34]},"properties":{"s":4,"d":270}}]}`,
		"nwps_points_lox_cg1_000.geojson": `{"features":[{"geometry":{"coordinates":[-118,34]},"properties":{"h":1,"s":0.8,"p":10,"d":270}}]}`,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	result := warmForecastCachesOnce(dir)
	if result.swell != 1 || result.wind != 1 || result.nwps != 1 {
		t.Fatalf("unexpected warm result: %+v", result)
	}

	// A second pass exercises the mtime-backed cache path rather than decoding
	// the same files again.
	result = warmForecastCachesOnce(dir)
	if result.swell != 1 || result.wind != 1 || result.nwps != 1 {
		t.Fatalf("unexpected cached warm result: %+v", result)
	}
}

func TestWarmForecastCachesFromBundles(t *testing.T) {
	dir := t.TempDir()
	writeMetadata(t, dir, `{"bundles":{"swell":"swell_r.bin","wind":"wind_r.bin"},`+
		`"nwps_points":[{"wfo":"lox","grid":"cg1","hours":[0,3],"file":"nwps_points_lox_cg1_r.bin"}]}`)
	lat, lon := []float64{34}, []float64{242}
	one := func(names ...string) (vars []bundleVar, values map[string][]float64) {
		values = map[string][]float64{}
		for _, n := range names {
			vars = append(vars, bundleVar{n, 0.1})
			values[n] = []float64{1}
		}
		return vars, values
	}
	swellVars, swellValues := one("h1", "p1", "d1", "h2", "p2", "d2", "h3", "p3", "d3")
	windVars, windValues := one("s", "d", "u", "v")
	nwpsVars, nwpsValues := one("h", "s", "p", "d")
	// Hour 1 isn't on the 3-hourly cadence the server samples; skip it.
	writeTestBundle(t, filepath.Join(dir, "swell_r.bin"), lat, lon, []int{0, 1, 3}, swellVars,
		map[int]map[string][]float64{0: swellValues, 1: swellValues, 3: swellValues})
	writeTestBundle(t, filepath.Join(dir, "wind_r.bin"), lat, lon, []int{0}, windVars,
		map[int]map[string][]float64{0: windValues})
	writeTestBundle(t, filepath.Join(dir, "nwps_points_lox_cg1_r.bin"), lat, lon, []int{0, 3}, nwpsVars,
		map[int]map[string][]float64{0: nwpsValues, 3: nwpsValues})

	result := warmForecastCachesOnce(dir)
	if result.swell != 2 || result.wind != 1 || result.nwps != 2 {
		t.Fatalf("unexpected warm result: %+v", result)
	}
}
