package main

import "testing"

func TestSpotExposure(t *testing.T) {
	b := beach{Facing: 237, HasFacing: true, OpenWater: [][2]float64{{184, 332}}}
	report := SpotReport{PrimaryDegrees: "240", SecondaryDegrees: "90", HasSecondary: true}
	e, ok := spotExposure(b, report, true)
	if !ok {
		t.Fatal("beach with a facing should have an exposure dial")
	}
	if e.Primary.Pct != 100 || e.Secondary.Pct != 0 {
		t.Errorf("head-on / from behind = %d%% / %d%%, want 100 / 0", e.Primary.Pct, e.Secondary.Pct)
	}
	// open water plus the taper, trimmed where exposure drops under 5%
	if e.Accepts != "171–343°" {
		t.Errorf("accepts %q", e.Accepts)
	}
	if e.LandPath != "M20.0 20.0 L12.3 4.9 A17 17 0 1 1 18.8 37.0 Z" || e.Facing != "WSW 237°" {
		t.Errorf("land %q facing %q", e.LandPath, e.Facing)
	}
	if _, ok := spotExposure(beach{}, report, true); ok {
		t.Error("no facing should mean no dial")
	}
}

func TestCircularRunsWraps(t *testing.T) {
	in := make([]bool, 360)
	for d := 300; d < 360; d++ {
		in[d] = true
	}
	for d := 0; d <= 20; d++ {
		in[d] = true
	}
	runs := circularRuns(in)
	if len(runs) != 1 || runs[0][0] != 300 || runs[0][1]%360 != 20 {
		t.Errorf("runs %v, want one run 300..20", runs)
	}
}
