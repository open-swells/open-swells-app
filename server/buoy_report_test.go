package main

import "testing"

func TestBuoyReportConvertsUnits(t *testing.T) {
	r := buoyReport(
		SwellReport{Date: "2026/10/22 03:56", WaveHeight: "0.9", SwellHeight: "0.8", SwellPeriod: "13.3", SwellDegrees: "225",
			WindSeaHeight: "0.5", WindSeaPeriod: "9.9", WindSeaDegrees: "248", Steepness: "VERY_STEEP"},
		WindReport{WindSpeed: "5.0", WindGust: "7.0", WindDir: "270", WaterTemp: "22.6"},
	)
	if r.Seas != "3.0" || r.SwellHeight != "2.6" || r.WindSeaHeight != "1.6" {
		t.Errorf("heights not converted to feet: %+v", r)
	}
	if r.WindSpeed != "11" || r.WindGust != "16" || !r.HasWind {
		t.Errorf("wind not converted to mph: %+v", r)
	}
	if r.Steepness != "very steep seas" || r.ObservedUnixMs == 0 {
		t.Errorf("steepness or observation time wrong: %+v", r)
	}

	empty := buoyReport(SwellReport{Steepness: "N/A"}, WindReport{})
	if empty.HasSeas || empty.HasSwell || empty.HasWindSea || empty.HasWind || empty.Steepness != "" {
		t.Errorf("missing observations should stay empty: %+v", empty)
	}
}
