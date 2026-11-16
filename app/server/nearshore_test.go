package main

import (
	"math"
	"testing"
)

func rss(comps []SwellComponent, windSea bool) float64 {
	e := 0.0
	for _, c := range comps {
		if c.WindSea == windSea {
			e += c.HeightM * c.HeightM
		}
	}
	return math.Sqrt(e)
}

func TestCalibrateNearshoreMatchesNwpsEnergy(t *testing.T) {
	comps := []SwellComponent{
		{HeightM: 0.4, PeriodS: 16.7, DirDeg: 276},
		{HeightM: 0.3, PeriodS: 9.4, DirDeg: 260},
		{HeightM: 0.4, PeriodS: 3.7, DirDeg: 266, WindSea: true},
	}
	hours := []hourSwells{{Comps: comps}}
	calibrateNearshore(hours, []*nwpsPoint{{H: 1.0, S: 0.9, P: 14.9, D: 267}})
	got := hours[0].Comps
	if s := rss(got, false); math.Abs(s-0.9) > 1e-9 {
		t.Errorf("swell energy = %.3f m, want 0.9", s)
	}
	if w := rss(got, true); math.Abs(w-math.Sqrt(1-0.81)) > 1e-9 {
		t.Errorf("wind sea = %.3f m, want %.3f", w, math.Sqrt(0.19))
	}
	if got[0].PeriodS != 16.7 || got[0].DirDeg != 276 || math.Abs(got[0].HeightM/got[1].HeightM-0.4/0.3) > 1e-9 {
		t.Errorf("partition shape changed: %+v", got)
	}
	if comps[0].HeightM != 0.4 {
		t.Error("input slice mutated")
	}
}

func TestCalibrateNearshoreTotalOnlyWithoutSwellHeight(t *testing.T) {
	hours := []hourSwells{{Comps: []SwellComponent{
		{HeightM: 0.3, PeriodS: 14},
		{HeightM: 0.4, PeriodS: 4, WindSea: true},
	}}}
	calibrateNearshore(hours, []*nwpsPoint{{H: 1.0}})
	if total := math.Hypot(rss(hours[0].Comps, false), rss(hours[0].Comps, true)); math.Abs(total-1.0) > 1e-9 {
		t.Errorf("total = %.3f m, want 1.0", total)
	}
}

func TestCalibrateNearshoreFadesPastHorizon(t *testing.T) {
	n := 30
	hours := make([]hourSwells, n)
	ns := make([]*nwpsPoint, n)
	for i := range hours {
		hours[i].Comps = []SwellComponent{{HeightM: 0.5, PeriodS: 14}}
	}
	ns[0] = &nwpsPoint{H: 1.0, S: 1.0} // factor 2, then NWPS ends
	calibrateNearshore(hours, ns)
	want := func(i int) float64 {
		if i >= nearshoreFadeHours {
			return 0.5
		}
		return 0.5 * (1 + float64(nearshoreFadeHours-i)/nearshoreFadeHours)
	}
	for i := range hours {
		if h := hours[i].Comps[0].HeightM; math.Abs(h-want(i)) > 1e-9 {
			t.Errorf("hour %d height = %.4f, want %.4f", i, h, want(i))
		}
	}
}

func TestCalibrateNearshoreNoNwpsLeavesTrains(t *testing.T) {
	hours := []hourSwells{{Comps: []SwellComponent{{HeightM: 0.5, PeriodS: 14}}}}
	calibrateNearshore(hours, []*nwpsPoint{nil})
	if hours[0].Comps[0].HeightM != 0.5 {
		t.Errorf("height = %v, want unchanged 0.5", hours[0].Comps[0].HeightM)
	}
}
