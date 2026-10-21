package main

import (
	"strings"
	"testing"
)

func TestSpotExposure(t *testing.T) {
	b := beach{Facing: 237, HasFacing: true, OpenWater: [][2]float64{{184, 332}}}
	report := SpotReport{PrimaryDegrees: "270", PrimaryHeight: "6.0", SecondaryDegrees: "x", SecondaryHeight: "2.0", HasSecondary: true}
	e, ok := spotExposure(b, report, true)
	if !ok {
		t.Fatal("beach with a facing should have an exposure map")
	}
	// one wedge over the open-water arc, 184–332°
	if e.ArcPath != "M50 50 L44.77 124.82 A75 75 0 0 1 14.79 -16.22 Z" {
		t.Errorf("arc %q", e.ArcPath)
	}
	// 6ft from due west: full size, tail at the left edge, tip just left of
	// the beach
	if len(e.Arrows) != 1 || e.Arrows[0].Kind != "primary" {
		t.Fatalf("arrows %+v, want the primary only (secondary has no direction)", e.Arrows)
	}
	if e.Arrows[0].Path != "M4.00 51.40 L4.00 48.60 L36.50 48.60 L36.50 45.50 L45.50 50.00 L36.50 54.50 L36.50 51.40 Z" {
		t.Errorf("primary arrow %q", e.Arrows[0].Path)
	}
	if e, _ := spotExposure(b, report, false); len(e.Arrows) != 0 {
		t.Error("no report should draw no arrows")
	}
	// a smaller secondary draws after (on top of) the primary; an equal or
	// bigger one under it
	report.SecondaryDegrees = "260"
	if e, _ := spotExposure(b, report, true); kinds(e.Arrows) != "primary secondary" {
		t.Errorf("smaller secondary: order %q", kinds(e.Arrows))
	}
	report.SecondaryHeight = "6.0"
	if e, _ := spotExposure(b, report, true); kinds(e.Arrows) != "secondary primary" {
		t.Errorf("equal secondary: order %q", kinds(e.Arrows))
	}
	if _, ok := spotExposure(beach{}, report, true); ok {
		t.Error("no facing should mean no map")
	}
}

func TestOpenArcPath(t *testing.T) {
	// a hand-tuned window is drawn as is
	if got := openArcPath(beach{Facing: 215, HasFacing: true, Window: []float64{170, 260}}); got != "M50 50 L63.02 123.86 A75 75 0 0 1 -23.86 63.02 Z" {
		t.Errorf("window arc %q", got)
	}
	// with only a facing, everything in front of the beach
	if got := openArcPath(beach{Facing: 270, HasFacing: true}); got != "M50 50 L50.00 125.00 A75 75 0 0 1 50.00 -25.00 Z" {
		t.Errorf("facing arc %q", got)
	}
}

func kinds(arrows []swellArrow) string {
	var k []string
	for _, a := range arrows {
		k = append(k, a.Kind)
	}
	return strings.Join(k, " ")
}

func TestSwellArrowScalesWithHeight(t *testing.T) {
	tests := []struct {
		ft, want float64
	}{
		{0.1, arrowMinScale}, // tiny trains keep a visible arrow
		{1.5, 0.5},
		{6, 1},
		{12, 1}, // big swell tops out at the square's edge
	}
	for _, tt := range tests {
		if got := arrowScale(tt.ft); got != tt.want {
			t.Errorf("arrowScale(%v) = %v, want %v", tt.ft, got, tt.want)
		}
	}
	// a 1.5ft train from due north: tail halfway out, at 50 - (4.5 + 41.5*0.5)
	if got := newSwellArrow("0", "1.5").Path; !strings.HasPrefix(got, "M48.95 24.75 ") {
		t.Errorf("1.5ft arrow %q", got)
	}
	if newSwellArrow("0", "").Has {
		t.Error("no height should draw no arrow")
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
