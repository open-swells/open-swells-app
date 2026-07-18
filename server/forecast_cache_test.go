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
