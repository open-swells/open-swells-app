package main

import (
	"testing"
	"time"
)

func TestClassifyHourRatings(t *testing.T) {
	facing := 270.0 // west-facing beach
	groundswell := []SwellComponent{{HeightM: 2.0, PeriodS: 15, DirDeg: 275}}

	cases := []struct {
		name    string
		comps   []SwellComponent
		windMs  float64
		windDir float64
		hasWind bool
		want    string
	}{
		{"solid groundswell, offshore wind", groundswell, 4, 90, true, "epic"},
		{"solid groundswell, moderate onshore", groundswell, 6, 270, true, "fair"},
		{"solid groundswell, strong onshore blows it out", groundswell, 11, 270, true, "poor"},
		// the El Porto case: overhead swell is still junk in a 20 mph seabreeze
		{"4ft swell, 20 mph onshore", []SwellComponent{{HeightM: 1.2, PeriodS: 14, DirDeg: 265}}, 9, 270, true, "poor"},
		{"4ft swell, 20 mph cross-shore", []SwellComponent{{HeightM: 1.2, PeriodS: 14, DirDeg: 265}}, 9, 0, true, "fair"},
		{"knee-high windswell, light wind", []SwellComponent{{HeightM: 0.5, PeriodS: 6, DirDeg: 280}}, 1.5, 200, true, "poor"},
		{"blocked swell from behind the point", []SwellComponent{{HeightM: 2.0, PeriodS: 15, DirDeg: 60}}, 1.5, 90, true, "poor"},
		{"mid-size swell, glassy", []SwellComponent{{HeightM: 1.4, PeriodS: 12, DirDeg: 260}}, 1, 0, true, "good"},
	}
	for _, tc := range cases {
		score, _ := classifyHour(tc.comps, facing, true, nil, tc.windMs, tc.windDir, tc.hasWind)
		if got := ratingFor(score); got != tc.want {
			t.Errorf("%s: got %s (score %.1f), want %s", tc.name, got, score, tc.want)
		}
	}
}

func TestOffshoreBeatsOnshore(t *testing.T) {
	comps := []SwellComponent{{HeightM: 1.5, PeriodS: 12, DirDeg: 270}}
	offshore, _ := classifyHour(comps, 270, true, nil, 5, 90, true)
	onshore, _ := classifyHour(comps, 270, true, nil, 5, 270, true)
	if offshore <= onshore {
		t.Errorf("offshore wind (%.1f) should outscore the same wind onshore (%.1f)", offshore, onshore)
	}
}

func TestCombinedSwellsBeatEither(t *testing.T) {
	a := []SwellComponent{{HeightM: 1.2, PeriodS: 13, DirDeg: 270}}
	b := []SwellComponent{{HeightM: 1.0, PeriodS: 10, DirDeg: 250}}
	both := append(append([]SwellComponent{}, a...), b...)
	sa, _ := classifyHour(a, 270, true, nil, 2, 90, true)
	sb, _ := classifyHour(b, 270, true, nil, 2, 90, true)
	sc, _ := classifyHour(both, 270, true, nil, 2, 90, true)
	if sc <= sa || sc <= sb {
		t.Errorf("combined swells (%.1f) should outscore either alone (%.1f, %.1f)", sc, sa, sb)
	}
}

func TestSwellWindowGatesDirection(t *testing.T) {
	window := []float64{170, 260} // e.g. Huntington
	inside := []SwellComponent{{HeightM: 1.5, PeriodS: 14, DirDeg: 200}}
	outside := []SwellComponent{{HeightM: 1.5, PeriodS: 14, DirDeg: 330}}
	si, _ := classifyHour(inside, 215, true, window, 2, 35, true)
	so, _ := classifyHour(outside, 215, true, window, 2, 35, true)
	if si <= so {
		t.Errorf("in-window swell (%.1f) should outscore blocked swell (%.1f)", si, so)
	}
	if ratingFor(so) != "poor" {
		t.Errorf("swell 70 degrees outside the window should rate poor, got %s (%.1f)", ratingFor(so), so)
	}
}

func TestCircularMid(t *testing.T) {
	cases := [][3]float64{{170, 260, 215}, {300, 60, 0}, {350, 10, 0}}
	for _, c := range cases {
		if got := circularMid(c[0], c[1]); got != c[2] {
			t.Errorf("circularMid(%v, %v) = %v, want %v", c[0], c[1], got, c[2])
		}
	}
}

func TestWindAtInterpolates(t *testing.T) {
	start := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	samples := []WindSample{{Hour: 0, Speed: 2, Dir: 350}, {Hour: 3, Speed: 4, Dir: 10}}
	s, d, ok := windAt(samples, start, start.Add(90*time.Minute))
	if !ok || s != 3 || d != 0 {
		t.Errorf("windAt midpoint = %.1f m/s @ %.0f (ok=%v), want 3.0 @ 0", s, d, ok)
	}
	if _, _, ok := windAt(samples, start, start.Add(12*time.Hour)); ok {
		t.Error("windAt far past the series should report no wind")
	}
}

func TestApplyConditionSummary(t *testing.T) {
	day := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	summary := []ForecastSummary{{Date: "2026-07-17", Condition: "fair"}}
	conds := []HourlyCondition{
		{UnixMs: day.UnixMilli(), Score: 20, Rating: "poor"},
		{UnixMs: day.Add(6 * time.Hour).UnixMilli(), Score: 85, Rating: "epic"},
	}
	applyConditionSummary(summary, conds)
	if summary[0].Condition != "epic" {
		t.Errorf("day condition should follow the best hour, got %s", summary[0].Condition)
	}
}
