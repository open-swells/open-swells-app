package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// fixtures are real SRF products captured 2026-07-15 from four offices with
// different field sets and layouts (SGX, LOX, MFL, PHI).
func loadSurfFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "surfzone", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}

var surfFixtureNow = time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC)

func TestParseSurfZoneSGX(t *testing.T) {
	fcs, err := parseSurfZoneProduct(loadSurfFixture(t, "caz043.txt"), "ca", surfFixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(fcs) != 1 {
		t.Fatalf("expected 1 zone, got %d", len(fcs))
	}
	f := fcs[0]
	if f.ZoneID != "CAZ043" {
		t.Errorf("zone id = %q", f.ZoneID)
	}
	if f.ZoneName != "San Diego County Coastal Areas" {
		t.Errorf("zone name = %q", f.ZoneName)
	}
	if f.Office != "KSGX" {
		t.Errorf("office = %q", f.Office)
	}
	if got := f.Issued.Format("2006-01-02 15:04"); got != "2026-07-15 18:31" {
		t.Errorf("issued = %s", got)
	}
	if got := f.Expires.Format("2006-01-02 15:04"); got != "2026-07-16 09:30" {
		t.Errorf("expires = %s", got)
	}
	if len(f.Headlines) != 2 {
		t.Errorf("headlines = %v", f.Headlines)
	}
	if len(f.Periods) != 2 {
		t.Fatalf("expected 2 periods, got %d: %+v", len(f.Periods), f.Periods)
	}
	p := f.Periods[0]
	if p.Name != "TODAY" {
		t.Errorf("period name = %q", p.Name)
	}
	if p.RipRisk != "Moderate" {
		t.Errorf("rip risk = %q", p.RipRisk)
	}
	if p.SurfHeight != "2 to 4 feet" {
		t.Errorf("surf height = %q", p.SurfHeight)
	}
	if p.SurfMinFt == nil || p.SurfMaxFt == nil || *p.SurfMinFt != 2 || *p.SurfMaxFt != 4 {
		t.Errorf("surf range = %v..%v", p.SurfMinFt, p.SurfMaxFt)
	}
	if p.WaterTemp != "68 to 73 degrees" {
		t.Errorf("water temp = %q", p.WaterTemp)
	}
	if len(p.Tides) != 4 {
		t.Errorf("tides = %v", p.Tides)
	}
	if p.Remarks == "" || p.Remarks[:5] != "Mixed" {
		t.Errorf("remarks = %q", p.Remarks)
	}
}

// LOX issues one segment covering two zones; both get a forecast with
// their own name.
func TestParseSurfZoneLOXMultiZone(t *testing.T) {
	fcs, err := parseSurfZoneProduct(loadSurfFixture(t, "caz349.txt"), "ca", surfFixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(fcs) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(fcs))
	}
	if fcs[0].ZoneID != "CAZ349" || fcs[1].ZoneID != "CAZ350" {
		t.Errorf("zone ids = %s, %s", fcs[0].ZoneID, fcs[1].ZoneID)
	}
	if fcs[0].ZoneName != "Santa Barbara County Southwestern Coast" {
		t.Errorf("zone 0 name = %q", fcs[0].ZoneName)
	}
	if fcs[1].ZoneName != "Santa Barbara County Southeastern Coast" {
		t.Errorf("zone 1 name = %q", fcs[1].ZoneName)
	}
	if len(fcs[0].Periods) != 2 {
		t.Fatalf("periods = %d", len(fcs[0].Periods))
	}
	if fcs[0].Periods[0].Name != "THIS AFTERNOON THROUGH THURSDAY" {
		t.Errorf("period name = %q", fcs[0].Periods[0].Name)
	}
}

// Florida offices add weather/heat fields and name beaches; extras land in
// Fields, and multi-line winds continue onto the same field.
func TestParseSurfZoneMFL(t *testing.T) {
	fcs, err := parseSurfZoneProduct(loadSurfFixture(t, "flz172.txt"), "fl", surfFixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	f := fcs[0]
	if f.ZoneID != "FLZ172" {
		t.Errorf("zone id = %q", f.ZoneID)
	}
	if f.Beaches != "Including the beaches of Fort Lauderdale" {
		t.Errorf("beaches = %q", f.Beaches)
	}
	p := f.Periods[0]
	if p.SurfMinFt == nil || p.SurfMaxFt == nil || *p.SurfMinFt != 0 || *p.SurfMaxFt != 1 {
		t.Errorf("surf range for %q = %v..%v", p.SurfHeight, p.SurfMinFt, p.SurfMaxFt)
	}
	if p.Fields["High Temperature"] == "" || p.Fields["Waterspout Risk"] == "" {
		t.Errorf("expected extra fields, got %v", p.Fields)
	}
	if len(p.Tides) == 0 {
		t.Errorf("expected tides, got none")
	}
	// second period's winds wrap over three lines
	w := f.Periods[1].Winds
	if !strings.Contains(w, "becoming") || !strings.Contains(w, "afternoon") {
		t.Errorf("winds = %q", w)
	}
}

func TestParseSurfZonePHI(t *testing.T) {
	fcs, err := parseSurfZoneProduct(loadSurfFixture(t, "njz014.txt"), "nj", surfFixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	f := fcs[0]
	if f.ZoneID != "NJZ014" || f.ZoneName != "Eastern Monmouth" {
		t.Errorf("zone = %s %q", f.ZoneID, f.ZoneName)
	}
	if f.Beaches != "Including the beaches of Sandy Hook" {
		t.Errorf("beaches = %q", f.Beaches)
	}
	if len(f.Periods) != 2 || f.Periods[0].Name != "THURSDAY" {
		t.Fatalf("periods = %+v", f.Periods)
	}
	p := f.Periods[0]
	if p.RipRisk != "Low" {
		t.Errorf("rip risk = %q", p.RipRisk)
	}
	if p.SurfMinFt == nil || *p.SurfMaxFt != 1 {
		t.Errorf("surf range = %v..%v for %q", p.SurfMinFt, p.SurfMaxFt, p.SurfHeight)
	}
	if p.Fields["UV Index"] != "High." {
		t.Errorf("uv index = %q", p.Fields["UV Index"])
	}
	// the footer's rip current definitions must not leak into fields
	last := f.Periods[len(f.Periods)-1]
	if _, ok := last.Fields["* Low Risk - The risk for rip currents is low, however,"]; ok {
		t.Error("footer leaked into fields")
	}
}

func TestSurfZoneHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := NewSurfZoneStore(nil, []string{"ca"})
	for _, fixture := range []struct{ file, state string }{
		{"caz043.txt", "ca"}, {"caz349.txt", "ca"}, {"njz014.txt", "nj"},
	} {
		fcs, err := parseSurfZoneProduct(loadSurfFixture(t, fixture.file), fixture.state, surfFixtureNow)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fcs {
			store.byZone[f.ZoneID] = f
		}
	}

	router := gin.New()
	router.GET("/api/beaches", store.handleList)
	router.GET("/api/beach/:zoneId", store.handleZone)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/beaches", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	var list []surfZoneSummary
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 { // CAZ043 + CAZ349 + CAZ350 + NJZ014
		t.Fatalf("expected 4 summaries, got %d", len(list))
	}
	if list[0].ZoneID != "CAZ043" || list[0].RipRisk != "Moderate" || list[0].SurfHeight != "2 to 4 feet" {
		t.Errorf("first summary = %+v", list[0])
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/beach/caz350", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("zone status = %d", w.Code)
	}
	var f SurfZoneForecast
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.ZoneID != "CAZ350" || len(f.Periods) != 2 || f.Raw == "" {
		t.Errorf("zone detail = %s, %d periods, raw len %d", f.ZoneID, len(f.Periods), len(f.Raw))
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/beach/CAZ999", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown zone status = %d", w.Code)
	}
}

func TestBeachPageTemplate(t *testing.T) {
	tmpl := loadTemplates()
	fcs, err := parseSurfZoneProduct(loadSurfFixture(t, "caz043.txt"), "ca", surfFixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "beach.html", fcs[0]); err != nil {
		t.Fatalf("render beach.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"San Diego County Coastal Areas",
		"SURF ZONE CAZ043",
		"Moderate",
		"2 to 4 feet",
		"Full NWS product text",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

func TestExpandUGCZones(t *testing.T) {
	cases := map[string][]string{
		"CAZ043-160930-":        {"CAZ043"},
		"CAZ349-350-161030-":    {"CAZ349", "CAZ350"},
		"CAZ340>342-87-161030-": {"CAZ340", "CAZ341", "CAZ342", "CAZ087"},
		"FLZ172-NJZ014-161000-": {"FLZ172", "NJZ014"},
	}
	for in, want := range cases {
		got := expandUGCZones(in)
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: got %v, want %v", in, got, want)
				break
			}
		}
	}
}

func TestParseSurfFeet(t *testing.T) {
	cases := []struct {
		in       string
		min, max float64
	}{
		{"2 to 4 feet.", 2, 4},
		{"Around 3 feet.", 3, 3},
		{"1 foot or less.", 0, 1},
		{"3 to 5 feet building to 5 to 8 feet in the afternoon.", 3, 8},
	}
	for _, c := range cases {
		min, max := parseSurfFeet(c.in)
		if min == nil || max == nil || *min != c.min || *max != c.max {
			t.Errorf("%q: got %v..%v, want %v..%v", c.in, min, max, c.min, c.max)
		}
	}
	if min, max := parseSurfFeet("Flat."); min != nil || max != nil {
		t.Errorf("Flat: got %v..%v, want nils", min, max)
	}
}
