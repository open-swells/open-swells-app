package main

import (
	"math"
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
		score, _ := classifyHour(tc.comps, beach{Facing: facing, HasFacing: true}, tc.windMs, tc.windDir, tc.hasWind)
		if got := ratingFor(score); got != tc.want {
			t.Errorf("%s: got %s (score %.1f), want %s", tc.name, got, score, tc.want)
		}
	}
}

func TestOffshoreBeatsOnshore(t *testing.T) {
	comps := []SwellComponent{{HeightM: 1.5, PeriodS: 12, DirDeg: 270}}
	offshore, _ := classifyHour(comps, beach{Facing: 270, HasFacing: true}, 5, 90, true)
	onshore, _ := classifyHour(comps, beach{Facing: 270, HasFacing: true}, 5, 270, true)
	if offshore <= onshore {
		t.Errorf("offshore wind (%.1f) should outscore the same wind onshore (%.1f)", offshore, onshore)
	}
}

func TestCombinedSwellsBeatEither(t *testing.T) {
	a := []SwellComponent{{HeightM: 1.2, PeriodS: 13, DirDeg: 270}}
	b := []SwellComponent{{HeightM: 1.0, PeriodS: 10, DirDeg: 250}}
	both := append(append([]SwellComponent{}, a...), b...)
	sa, _ := classifyHour(a, beach{Facing: 270, HasFacing: true}, 2, 90, true)
	sb, _ := classifyHour(b, beach{Facing: 270, HasFacing: true}, 2, 90, true)
	sc, _ := classifyHour(both, beach{Facing: 270, HasFacing: true}, 2, 90, true)
	if sc <= sa || sc <= sb {
		t.Errorf("combined swells (%.1f) should outscore either alone (%.1f, %.1f)", sc, sa, sb)
	}
}

func TestSwellWindowGatesDirection(t *testing.T) {
	window := []float64{170, 260} // e.g. Huntington
	inside := []SwellComponent{{HeightM: 1.5, PeriodS: 14, DirDeg: 200}}
	outside := []SwellComponent{{HeightM: 1.5, PeriodS: 14, DirDeg: 330}}
	si, _ := classifyHour(inside, beach{Facing: 215, HasFacing: true, Window: window}, 2, 35, true)
	so, _ := classifyHour(outside, beach{Facing: 215, HasFacing: true, Window: window}, 2, 35, true)
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
	applyConditionSummary(summary, conds, time.UTC)
	if summary[0].Condition != "epic" {
		t.Errorf("day condition should follow the best hour, got %s", summary[0].Condition)
	}
	if summary[0].Score != 85 {
		t.Errorf("day score should follow the best hour, got %d", summary[0].Score)
	}
	// A series entirely in the past falls back to its start.
	candles := currentConditionCandles(conds)
	if len(candles) != 2 || candles[1].Score != 85 {
		t.Errorf("unexpected current condition candles: %+v", candles)
	}

	// A series spanning now supplies the surrounding 48 hours so the client
	// can select midnight-to-midnight in its local timezone.
	nowHour := time.Now().UTC().Truncate(time.Hour)
	var series []HourlyCondition
	for i := -30; i < 30; i++ {
		series = append(series, HourlyCondition{UnixMs: nowHour.Add(time.Duration(i) * time.Hour).UnixMilli(), Score: 50, Rating: "fair"})
	}
	windowed := conditionCandlesAround(series, nowHour)
	if len(windowed) != 48 {
		t.Fatalf("windowed candles length = %d, want 48", len(windowed))
	}
	first := windowed[0].UnixMs
	if first != nowHour.Add(-24*time.Hour).UnixMilli() {
		t.Errorf("windowed candles should start 24 hours before now, got %d", first)
	}
}

func TestBreakerHeightKomarGaughan(t *testing.T) {
	// Hb = 0.39 g^0.2 (T H0^2)^0.4: 1 m at 10 s breaks at ~1.55 m.
	if got := breakerHeightM(1, 10); math.Abs(got-1.547) > 0.005 {
		t.Errorf("breakerHeightM(1, 10) = %.3f, want ~1.547", got)
	}
	// Same offshore height, longer period: bigger surf.
	if breakerHeightM(1, 16) <= breakerHeightM(1, 8) {
		t.Error("longer period should break bigger")
	}
	if breakerHeightM(0, 12) != 0 || breakerHeightM(1, 0) != 0 {
		t.Error("no height or no period means no surf")
	}
}

func TestSurfLabel(t *testing.T) {
	cases := []struct {
		lo, hi float64
		want   string
	}{
		{0.3, 0.5, "<1ft"},
		{0.7, 1.0, "1–2ft"},
		{2.8, 3.1, "3–4ft"},
		{3.2, 4.6, "3–5ft"},
	}
	for _, c := range cases {
		if got := surfLabel(c.lo, c.hi); got != c.want {
			t.Errorf("surfLabel(%v, %v) = %q, want %q", c.lo, c.hi, got, c.want)
		}
	}
}

func TestSurfRangeBlocksAndBreaks(t *testing.T) {
	lo, hi, _ := surfRange([]SwellComponent{{HeightM: 1, PeriodS: 14, DirDeg: 270}}, beach{Facing: 270, HasFacing: true})
	// 1 m @ 14 s head-on: ~1.77 m breaking, so ~4-6 ft.
	if hi < 5.5 || hi > 6.1 || math.Abs(lo-hi*surfLowFactor) > 1e-9 {
		t.Errorf("1m@14s head-on = %.1f-%.1f ft, want ~4.1-5.8", lo, hi)
	}
	if _, hi, _ := surfRange([]SwellComponent{{HeightM: 2, PeriodS: 15, DirDeg: 90}}, beach{Facing: 270, HasFacing: true}); hi != 0 {
		t.Errorf("swell from behind the beach should not break, got %.1f ft", hi)
	}
}

func TestWindSeaMakesSurf(t *testing.T) {
	// An enclosed sea with no swell, only a local wind sea, still has surf.
	comps := []SwellComponent{{HeightM: 0.8, PeriodS: 4.5, DirDeg: 270, WindSea: true}}
	_, surf := classifyHour(comps, beach{Facing: 270, HasFacing: true}, 2, 90, true)
	if surf.HiFt < 2 {
		t.Errorf("0.8 m wind sea should make surf, got %.1f ft", surf.HiFt)
	}
}

func TestWindSeaIsNotTheSeawardStandIn(t *testing.T) {
	// No facing: the swell (from the west) marks the sea. A bigger wind
	// sea runs east-to-west with an easterly wind, which is offshore here;
	// using the wind sea as the stand-in would score it onshore.
	swell := SwellComponent{HeightM: 1.2, PeriodS: 13, DirDeg: 270}
	windSea := SwellComponent{HeightM: 1.5, PeriodS: 5, DirDeg: 90, WindSea: true}
	flagged, _ := classifyHour([]SwellComponent{swell, windSea}, beach{}, 7, 90, true)
	windSea.WindSea = false
	unflagged, _ := classifyHour([]SwellComponent{swell, windSea}, beach{}, 7, 90, true)
	if flagged <= unflagged {
		t.Errorf("easterly wind should score as offshore (%.1f) not onshore (%.1f)", flagged, unflagged)
	}
}

func TestApplyConditionSummaryUsesSurfRange(t *testing.T) {
	day := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	summary := []ForecastSummary{{Date: "2026-07-17", WaveHeight: "2.1ft"}}
	conds := []HourlyCondition{
		{UnixMs: day.UnixMilli(), Score: 40, Rating: "fair", Surf: "2–3ft", SurfHiFt: 2.6},
		{UnixMs: day.Add(3 * time.Hour).UnixMilli(), Score: 60, Rating: "good", Surf: "3–4ft", SurfHiFt: 3.8},
	}
	applyConditionSummary(summary, conds, time.UTC)
	if summary[0].WaveHeight != "3–4ft" || summary[0].HeightFt != 3.8 {
		t.Errorf("day height should be the best hour's surf, got %q (%.1f)", summary[0].WaveHeight, summary[0].HeightFt)
	}
}

// huntington is the coastline-derived exposure spot_facing.py computes for
// Huntington Beach: facing SW into Catalina's shadow, with open water to
// the south and a western slot between Catalina and Palos Verdes.
var huntington = beach{Facing: 223, HasFacing: true, OpenWater: [][2]float64{{134, 214}, {250, 284}}}

func TestOpenWaterGatesBlockedSwell(t *testing.T) {
	if got := swellExposure(30, huntington); got != 0 {
		t.Errorf("swell from over land should be blocked, got %.2f", got)
	}
	// Just inside Catalina's shadow: partly wrapping in from the near edge.
	shadow := swellExposure(225, huntington)
	if shadow <= 0 || shadow >= 0.5 {
		t.Errorf("swell 11 deg into the shadow should be mostly blocked, got %.2f", shadow)
	}
	if got := swellExposure(232, huntington); got != 0 {
		t.Errorf("swell from mid-shadow should be blocked, got %.2f", got)
	}
}

func TestOpenWaterObliquityUsesNearestOpenDirection(t *testing.T) {
	// The facing (223) is in the shadow, so swell arrives square from the
	// nearest open direction (214): a south swell enters at full strength.
	if got := swellExposure(185, huntington); got != 1 {
		t.Errorf("south swell through open water = %.2f, want 1", got)
	}
	// Through the western slot, 51 deg off that reference: partial.
	west := swellExposure(265, huntington)
	if west <= 0.5 || west >= 1 {
		t.Errorf("west swell through the slot = %.2f, want partial", west)
	}
	// A facing inside open water is its own reference.
	open := beach{Facing: 180, HasFacing: true, OpenWater: [][2]float64{{90, 270}}}
	if got := obliquityReference(open); got != 180 {
		t.Errorf("open facing reference = %v, want 180", got)
	}
}

func TestOpenWaterAllAround(t *testing.T) {
	islet := beach{Facing: 90, HasFacing: true, OpenWater: [][2]float64{{0, 360}}}
	if got := swellExposure(270, islet); got != 0 {
		// Open all around, but a swell from straight behind the beach
		// still doesn't reach this side of it.
		t.Errorf("swell from behind = %.2f, want 0", got)
	}
	if got := swellExposure(90, islet); got != 1 {
		t.Errorf("swell head-on = %.2f, want 1", got)
	}
}

func TestHandTunedWindowIgnoresFacingObliquity(t *testing.T) {
	// A curated window is the effective window: full energy anywhere in it.
	b := beach{Facing: 215, HasFacing: true, Window: []float64{170, 260}}
	if got := swellExposure(172, b); got != 1 {
		t.Errorf("swell at the window edge = %.2f, want 1", got)
	}
}

func TestObliquityReferenceIgnoresNarrowSlots(t *testing.T) {
	// Uluwatu: the shoreline faces NW (321) past a narrow open slot
	// (298-310), but its swell window is the wide SW arc (202-274). SW
	// swell must not be judged as oblique to the slot.
	uluwatu := beach{Facing: 321, HasFacing: true, OpenWater: [][2]float64{{298, 310}, {202, 274}}}
	if got := obliquityReference(uluwatu); got != 274 {
		t.Errorf("reference = %v, want the wide arc's near edge 274", got)
	}
	if got := swellExposure(225, uluwatu); got < 0.8 {
		t.Errorf("SW swell at Uluwatu = %.2f, want most of it", got)
	}
}
