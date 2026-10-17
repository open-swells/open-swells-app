package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSpotTidesFixture(t *testing.T, dir, body string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, spotTidesFile))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSpotTideTrimsEventsToWindowPlusNeighbors(t *testing.T) {
	dir := t.TempDir()
	// events every 6 hours from 00Z on 2026-07-11
	writeSpotTidesFixture(t, dir, `{
		"stations": {"9410230": {"name": "La Jolla", "type": "R", "hilo": [
			[1783728000, 0.1, "L"], [1783749600, 1.5, "H"], [1783771200, 0.4, "L"],
			[1783792800, 1.7, "H"], [1783814400, 0.0, "L"]
		], "hourly": {"start": 1783728000, "v": [
			0.1, 0.3, 0.6, 0.9, 1.2, 1.4, 1.5, 1.4, 1.2, 1.0, 0.8, 0.6, 0.4,
			0.5, 0.7, 0.9, 1.1, 1.3, 1.5, 1.6, 1.7, 1.6, 1.4, 1.2, 1.0
		]}}},
		"spots": {"blacks-beach": {"station": "9410230", "km": 2.5}}
	}`)
	base := time.Unix(1783728000, 0).UTC()

	tide, ok := spotTide(dir, "blacks-beach", base.Add(7*time.Hour), base.Add(13*time.Hour))
	if !ok {
		t.Fatal("expected a tide")
	}
	if tide.StationID != "9410230" || tide.StationName != "La Jolla" || tide.Km != 2.5 || tide.Subordinate {
		t.Errorf("station = %+v", tide)
	}
	// 06Z (before), 12Z (inside), 18Z (after)
	if len(tide.Events) != 3 {
		t.Fatalf("events = %+v", tide.Events)
	}
	first := tide.Events[0]
	if first.T != base.Add(6*time.Hour).UnixMilli() || first.V != 1.5 || !first.High {
		t.Errorf("first event = %+v", first)
	}
	if last := tide.Events[2]; last.T != base.Add(18*time.Hour).UnixMilli() {
		t.Errorf("last event = %+v", last)
	}
	// hourly values spanning the events, 06Z through 18Z
	if tide.Hourly == nil || tide.Hourly.Start != base.Add(6*time.Hour).UnixMilli() || len(tide.Hourly.V) != 13 {
		t.Fatalf("hourly = %+v", tide.Hourly)
	}
	if tide.Hourly.V[0] != 1.5 || tide.Hourly.V[12] != 1.5 {
		t.Errorf("hourly values = %v", tide.Hourly.V)
	}

	if _, ok := spotTide(dir, "unmatched", base, base.Add(time.Hour)); ok {
		t.Error("spot without a station should have no tide")
	}
	if _, ok := spotTide(dir, "blacks-beach", base.Add(48*time.Hour), base.Add(72*time.Hour)); ok {
		t.Error("window past the predictions should have no tide")
	}
}

func TestSpotTideMissingFile(t *testing.T) {
	if _, ok := spotTide(t.TempDir(), "blacks-beach", time.Now(), time.Now().Add(time.Hour)); ok {
		t.Error("expected no tide without a file")
	}
}
