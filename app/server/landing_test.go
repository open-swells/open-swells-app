package main

import (
	"bytes"
	"testing"
	"time"
)

func TestRankLandingSpotsPrefersRideableThenTide(t *testing.T) {
	spots := []LandingSpot{
		{ID: "flat", Rating: "poor", Score: 90, SurfHiFt: 1, HasTide: true, Daylight: true},
		{ID: "no-tide", Rating: "good", Score: 80, SurfHiFt: 4, Daylight: true},
		{ID: "tide-fair", Rating: "fair", Score: 50, SurfHiFt: 3, HasTide: true, Daylight: true},
		{ID: "dark", Rating: "epic", Score: 95, SurfHiFt: 8, HasTide: true},
		{ID: "tide-good", Rating: "good", Score: 70, SurfHiFt: 5, HasTide: true, Daylight: true},
		{ID: "ankle", Rating: "fair", Score: 60, SurfHiFt: 1.5, HasTide: true, Daylight: true},
	}
	rankLandingSpots(spots)
	var got []string
	for _, s := range spots {
		got = append(got, s.ID)
	}
	want := []string{"tide-good", "tide-fair", "no-tide", "dark", "flat", "ankle"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranking = %v, want %v", got, want)
		}
	}
}

func TestSunElevation(t *testing.T) {
	// San Diego, Oct 23: noon local (19:00 UTC) is daytime, 3am is night.
	noon := time.Date(2026, 10, 23, 19, 0, 0, 0, time.UTC)
	night := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	if e := sunElevation(33, -117.3, noon); e < 40 || e > 50 {
		t.Errorf("noon elevation = %.1f, want about 45", e)
	}
	if e := sunElevation(33, -117.3, night); e > -30 {
		t.Errorf("3am elevation = %.1f, want well below the horizon", e)
	}
}

func TestLandingRendersLiveReport(t *testing.T) {
	tmpl := loadTemplates("../web/templates")
	live := LandingReport{
		ID: "pipeline", HasSpot: true,
		Spot: SpotPageData{
			TimeZone: "Pacific/Honolulu", SpotName: "Pipeline", Region: "USA, Hawaii, Oahu", HasForecast: true,
			SwellReport:     SwellReport{StationId: "pipeline"},
			Report:          SpotReport{PrimaryHeight: "7.5", PrimaryPeriod: "15.7", PrimaryDegrees: "315", Surf: "6–8ft"},
			HasReport:       true,
			ForecastSummary: []ForecastSummary{{Date: "2026-10-23", Condition: "good", WaveHeight: "6–8ft"}},
		},
	}
	var page bytes.Buffer
	if err := tmpl.ExecuteTemplate(&page, "landing.html", LandingPageData{SpotCount: 2, BuoyCount: 1, Live: live}); err != nil {
		t.Fatalf("landing template failed to render: %v", err)
	}
	for _, want := range []string{
		`id="spotSearch"`,
		`window.createForecastClock("Pacific/Honolulu")`,
		`data-spot-timezone`,
		`href="/surf-spots/pipeline"`,
		`6–8ft`,
		`16 day forecast`,
		`id="live" class="live-card"`,
		`d3@7`,
	} {
		if !bytes.Contains(page.Bytes(), []byte(want)) {
			t.Errorf("landing page is missing %q", want)
		}
	}
}
