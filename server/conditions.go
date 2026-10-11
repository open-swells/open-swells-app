package main

// Hourly surf-condition classifier. Each forecast hour gets a surf height
// (breaking wave height from every wave train that reaches the beach) and a
// 0-100 score: that height, scaled by the trains' period quality and a wind
// quality factor (glassy or offshore up, onshore down). Scores bucket into
// poor / fair / good / epic.
//
// Directions follow the marine convention throughout: degrees true, the
// direction the swell or wind comes FROM. A beach's "facing" is the bearing
// toward open water, so a swell whose direction equals the facing hits the
// beach head-on, and a wind whose direction is opposite the facing blows
// offshore (land to sea).

import (
	"fmt"
	"math"
	"time"
)

type SwellComponent struct {
	HeightM float64 // significant height, meters
	PeriodS float64
	DirDeg  float64 // direction the swell comes from, degrees true
	WindSea bool    // locally generated wind sea rather than swell
}

// hourSwells is one forecast hour's swell trains, sampled for a spot.
type hourSwells struct {
	Time  time.Time
	Comps []SwellComponent
}

// HourlyCondition is one hour of the classified forecast, shaped for the
// spot page's condition strip (times as epoch ms for the client).
type HourlyCondition struct {
	UnixMs int64  `json:"t"`
	Score  int    `json:"score"`
	Rating string `json:"rating"`
	// Breaking surf height range, feet (see surfRange), and its display
	// label, e.g. "3–4ft".
	SurfLoFt float64 `json:"ft_lo"`
	SurfHiFt float64 `json:"ft_hi"`
	Surf     string  `json:"surf"`
	WindMph  float64 `json:"wmph"`
	WindDir  float64 `json:"wdir"` // direction wind comes from, degrees true
	HasWind  bool    `json:"haswind"`
}

// angDiff returns the smallest absolute angle between two bearings, 0..180.
func angDiff(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 360)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// circularMid is the bearing halfway along the clockwise arc from a to b,
// e.g. the direction a spot with swell window [a, b] faces.
func circularMid(a, b float64) float64 {
	width := math.Mod(b-a+360, 360)
	return math.Mod(a+width/2+360, 360)
}

// beach is what the classifier knows about a spot's exposure.
type beach struct {
	// Facing is the bearing (degrees true) the beach faces, toward open
	// water; it judges wind (offshore vs onshore) and swell obliquity.
	Facing    float64
	HasFacing bool
	// Window is a hand-tuned [from, to] effective swell window. It decides
	// swell exposure on its own.
	Window []float64
	// OpenWater is the coastline-derived arcs of open sea, [from, to]
	// clockwise (see grib-parse-collect/spot_facing.py). Swell from outside
	// them is blocked by land; swell inside still loses energy the more
	// obliquely it meets the beach.
	OpenWater [][2]float64
}

// openWaterTaperDeg is how far beyond an open-water arc's edge swell still
// arrives, fading to nothing: it diffracts and refracts around the land
// that bounds the arc.
const openWaterTaperDeg = 15.0

// arcPast is how many degrees dir lies beyond arc [from, to] (clockwise),
// 0 inside it. An arc whose ends meet ([0, 360]) is the whole circle.
func arcPast(dir float64, arc [2]float64) float64 {
	width := math.Mod(arc[1]-arc[0]+360, 360)
	if width == 0 {
		return 0
	}
	into := math.Mod(dir-arc[0]+360, 360)
	if into <= width {
		return 0
	}
	return math.Min(into-width, 360-into) // degrees beyond the nearer edge
}

// swellExposure is how much of a train the beach receives, 0..1.
//
// A hand-tuned swell window is authoritative: full energy inside, tapering
// to zero 45 degrees beyond its edges. Otherwise a facing gives a generous
// acceptance cone (refraction bends swell toward shore, so even oblique
// trains deliver some energy), and open-water arcs gate out swell that
// land blocks. With none of these, everything passes.
func swellExposure(dir float64, b beach) float64 {
	if len(b.Window) == 2 {
		past := arcPast(dir, [2]float64{b.Window[0], b.Window[1]})
		return math.Max(0, 1-past/45)
	}
	exposure := 1.0
	if len(b.OpenWater) > 0 {
		past := 360.0
		for _, arc := range b.OpenWater {
			past = math.Min(past, arcPast(dir, arc))
		}
		exposure = math.Max(0, 1-past/openWaterTaperDeg)
	}
	if b.HasFacing && exposure > 0 {
		exposure *= obliquity(dir, obliquityReference(b))
	}
	return exposure
}

// obliquityReference is the direction swell is judged against for how
// squarely it meets the beach: the facing, unless land blocks it (a beach
// facing across a bay, or into an island's shadow). Swell then reaches the
// beach by bending in from the main body of open water, so the reference
// is the nearer edge of the widest open arc. (The nearest edge of any arc
// could be a sliver between islands, which would mark the beach's real
// swell window as oblique.)
func obliquityReference(b beach) float64 {
	if len(b.OpenWater) == 0 {
		return b.Facing
	}
	widest, widestWidth := b.OpenWater[0], -1.0
	for _, arc := range b.OpenWater {
		if arcPast(b.Facing, arc) == 0 {
			return b.Facing
		}
		width := math.Mod(arc[1]-arc[0]+360, 360)
		if width > widestWidth {
			widest, widestWidth = arc, width
		}
	}
	if angDiff(b.Facing, widest[1]) < angDiff(b.Facing, widest[0]) {
		return widest[1]
	}
	return widest[0]
}

// obliquity is the share of a train that reaches a beach facing `facing`
// from direction dir.
func obliquity(dir, facing float64) float64 {
	diff := angDiff(dir, facing)
	switch {
	case diff <= 35:
		return 1
	case diff <= 90:
		return 1 - (diff-35)/55*0.65 // 1 -> 0.35
	case diff <= 125:
		return 0.35 * (1 - (diff-90)/35) // 0.35 -> 0
	default:
		return 0
	}
}

// periodQuality weights a train's contribution to the score by period:
// short wind chop breaks weak and crumbly, long-period groundswell lines up
// and peels. (Period's effect on size is already in breakerHeightM; this
// is only about shape.)
func periodQuality(p float64) float64 {
	switch {
	case p <= 5:
		return 0.6
	case p <= 9:
		return 0.6 + (p-5)/4*0.25 // -> 0.85
	case p <= 13:
		return 0.85 + (p-9)/4*0.15 // -> 1.0
	default:
		return math.Min(1.05, 1+(p-13)*0.0125)
	}
}

const (
	gravity    = 9.80665
	metersToFt = 3.28084
	// Surf ranges run from the typical wave to the sets. breakerHeightM is
	// the breaking *significant* height (the mean of the highest third),
	// which is what surfers call set waves; the typical wave is smaller
	// (the Rayleigh mean height is 0.63 Hs; surfers discount the smallest
	// waves, so 0.7). Both are tuning knobs for verification against
	// observed surf.
	surfLowFactor  = 0.7
	surfHighFactor = 1.0
)

// breakerHeightM is the height (m) at which a train of deep-water
// significant height h0 (m) and period t (s) breaks, after Komar & Gaughan
// (1972): Hb = 0.39 g^(1/5) (T H0^2)^(2/5). Longer periods carry more
// energy per unit height and shoal up more before breaking, so the same
// offshore height makes bigger surf at 16 s than at 8 s.
func breakerHeightM(h0, t float64) float64 {
	if h0 <= 0 || t <= 0 {
		return 0
	}
	return 0.39 * math.Pow(gravity, 0.2) * math.Pow(t*h0*h0, 0.4)
}

// surfRange is the beach's breaking surf height range in feet, with the
// score's period-quality weight. Each train is first cut by how squarely
// it enters the beach (swellExposure, treated as a height coefficient like
// refraction), then broken with breakerHeightM; trains combine by
// root-sum-square as their energies add. quality is the period quality
// averaged by each train's share of breaking energy (1 when flat).
func surfRange(comps []SwellComponent, b beach) (loFt, hiFt, quality float64) {
	energy, weighted := 0.0, 0.0
	for _, c := range comps {
		hb := breakerHeightM(c.HeightM*swellExposure(c.DirDeg, b), c.PeriodS)
		energy += hb * hb
		weighted += hb * hb * periodQuality(c.PeriodS)
	}
	if energy == 0 {
		return 0, 0, 1
	}
	hb := math.Sqrt(energy) * metersToFt
	return hb * surfLowFactor, hb * surfHighFactor, weighted / energy
}

// surfLabel renders a range as surfers read it: whole feet, at least a
// foot wide ("3–4ft"), or "<1ft" when there is nothing to ride.
func surfLabel(loFt, hiFt float64) string {
	if hiFt < 1 {
		return "<1ft"
	}
	lo, hi := math.Max(1, math.Round(loFt)), math.Round(hiFt)
	if hi <= lo {
		hi = lo + 1
	}
	return fmt.Sprintf("%.0f–%.0fft", lo, hi)
}

// waveScore maps a buoy's average swell height (ft) to 0-100 with
// diminishing returns, for the day summaries of forecasts without the
// classifier.
func waveScore(ft float64) float64 {
	return scoreCurve(ft, [][2]float64{{0, 0}, {1, 14}, {2, 34}, {3, 50}, {4, 62}, {6, 78}, {8, 87}, {10, 92}, {14, 97}})
}

// surfScore maps breaking surf height (ft, the top of the range) to 0-100
// with diminishing returns: the jump from flat to chest-high matters more
// than overhead to well-overhead.
func surfScore(ft float64) float64 {
	return scoreCurve(ft, [][2]float64{{0, 0}, {1.5, 14}, {3, 34}, {4.5, 50}, {6, 62}, {9, 78}, {12, 87}, {15, 92}, {20, 97}})
}

// scoreCurve interpolates ft along ascending (ft, score) anchors.
func scoreCurve(ft float64, anchors [][2]float64) float64 {
	if ft >= anchors[len(anchors)-1][0] {
		return anchors[len(anchors)-1][1]
	}
	for i := 1; i < len(anchors); i++ {
		if ft <= anchors[i][0] {
			a, b := anchors[i-1], anchors[i]
			return a[1] + (b[1]-a[1])*(ft-a[0])/(b[0]-a[0])
		}
	}
	return 0
}

// windQuality scales the wave score by wind state. seaward is the bearing
// toward open water (the facing, or the dominant swell direction as a
// stand-in). Wind FROM seaward is onshore and degrades the surface; wind
// from the opposite direction is offshore and grooms it, until it gets
// strong enough to make paddling in miserable.
func windQuality(speedKts, windDir, seaward float64) float64 {
	// -1 = straight offshore, +1 = straight onshore
	onshore := math.Cos(angDiff(windDir, seaward) * math.Pi / 180)
	if speedKts < 4 { // glassy regardless of direction
		return 1.03
	}
	// Wind past ~7 kts tears surf apart quickly; by ~21 kts straight
	// onshore it's blown out no matter how big the swell is. The
	// directional weight fades the penalty from full (onshore) through
	// a bit over half (cross-shore chop) to none (straight offshore).
	ramp := math.Min(math.Max((speedKts-3)/18, 0), 1) * 0.95
	weight := 0.55 * (1 + onshore) // cross-shore -> 0.55, offshore -> 0
	if onshore > 0 {
		weight = 0.55 + 0.45*onshore // -> 1 straight onshore
	}
	f := 1 - ramp*weight
	if onshore < 0 {
		// offshore grooming bonus, fading into a paddling penalty once
		// a hard offshore starts blowing the tops off
		f += -onshore * (0.12*math.Min(speedKts/10, 1) - math.Max(0, speedKts-14)*0.025)
	}
	return math.Min(1.15, math.Max(0.1, f))
}

func ratingFor(score float64) string {
	switch {
	case score >= 80:
		return "epic"
	case score >= 55:
		return "good"
	case score >= 28:
		return "fair"
	default:
		return "poor"
	}
}

// windAt linearly interpolates the 3-hourly wind series to time t.
func windAt(samples []WindSample, start time.Time, t time.Time) (speedMs, dir float64, ok bool) {
	if len(samples) == 0 {
		return 0, 0, false
	}
	h := t.Sub(start).Hours()
	for i, s := range samples {
		if float64(s.Hour) >= h {
			if i == 0 || float64(s.Hour) == h {
				if math.Abs(float64(s.Hour)-h) > 3 {
					return 0, 0, false
				}
				return s.Speed, s.Dir, true
			}
			prev := samples[i-1]
			gap := float64(s.Hour - prev.Hour)
			if gap > 3 { // series has a hole here; don't bridge it
				return 0, 0, false
			}
			frac := (h - float64(prev.Hour)) / gap
			return prev.Speed + (s.Speed-prev.Speed)*frac, lerpAngle(prev.Dir, s.Dir, frac), true
		}
	}
	last := samples[len(samples)-1]
	if h-float64(last.Hour) > 3 {
		return 0, 0, false
	}
	return last.Speed, last.Dir, true
}

// surfHeight is one hour's breaking surf range, in feet.
type surfHeight struct {
	LoFt, HiFt float64
}

// classifyHour scores one hour of swell + wind for a beach.
func classifyHour(comps []SwellComponent, b beach,
	windSpeedMs, windDir float64, hasWind bool) (score float64, surf surfHeight) {

	lo, hi, quality := surfRange(comps, b)
	surf = surfHeight{LoFt: lo, HiFt: hi}
	score = surfScore(hi) * quality

	// Wind is judged against the facing; without one, the dominant swell's
	// direction stands in (swell comes from the sea, so wind opposing it
	// is offshore-ish — the user-visible "offshore effect"). Wind sea is
	// skipped: it runs with the local wind, so it would make any wind
	// look onshore.
	seaward, hasSeaward := b.Facing, b.HasFacing
	if !hasSeaward {
		bestH := 0.0
		for _, c := range comps {
			if !c.WindSea && c.HeightM > bestH {
				bestH, seaward, hasSeaward = c.HeightM, c.DirDeg, true
			}
		}
	}
	switch {
	case hasWind && hasSeaward:
		score *= windQuality(windSpeedMs*1.94384, windDir, seaward)
	case !hasWind:
		score *= 0.92 // unknown wind: assume it isn't perfect out there
	}
	return math.Min(100, math.Max(0, score)), surf
}

// conditionSeries classifies every forecast hour for a spot.
func conditionSeries(hours []hourSwells, wind []WindSample, windStart time.Time, hasWindStart bool,
	b beach) []HourlyCondition {

	out := make([]HourlyCondition, 0, len(hours))
	for _, h := range hours {
		var speedMs, dir float64
		hasWind := false
		if hasWindStart {
			speedMs, dir, hasWind = windAt(wind, windStart, h.Time)
		}
		score, surf := classifyHour(h.Comps, b, speedMs, dir, hasWind)
		out = append(out, HourlyCondition{
			UnixMs:   h.Time.UnixMilli(),
			Score:    int(math.Round(score)),
			Rating:   ratingFor(score),
			SurfLoFt: math.Round(surf.LoFt*10) / 10,
			SurfHiFt: math.Round(surf.HiFt*10) / 10,
			Surf:     surfLabel(surf.LoFt, surf.HiFt),
			WindMph:  math.Round(speedMs * 2.23694),
			WindDir:  dir,
			HasWind:  hasWind,
		})
	}
	return out
}

// applyConditionSummary upgrades the daily summary's height-only condition
// to the classifier's view of each day: a day is as good as its best hour,
// and its height is that hour's surf range.
func applyConditionSummary(summary []ForecastSummary, conds []HourlyCondition) {
	best := map[string]HourlyCondition{}
	for _, c := range conds {
		day := time.UnixMilli(c.UnixMs).UTC().Format("2006-01-02")
		if b, ok := best[day]; !ok || c.Score > b.Score {
			best[day] = c
		}
	}
	for i := range summary {
		if c, ok := best[summary[i].Date]; ok {
			summary[i].Condition = ratingFor(float64(c.Score))
			summary[i].Score = c.Score
			if c.Surf != "" {
				summary[i].WaveHeight = c.Surf
				summary[i].HeightFt = c.SurfHiFt
			}
		}
	}
}

// currentConditionCandles supplies enough hours on either side of now for
// the browser to build the viewer's local calendar day (midnight to
// midnight). The forecast run may not contain earlier hours from today; the
// client renders those as unavailable instead of shifting the strip forward.
func currentConditionCandles(conds []HourlyCondition) []ConditionCandle {
	return conditionCandlesAround(conds, time.Now())
}

func conditionCandlesAround(conds []HourlyCondition, now time.Time) []ConditionCandle {
	if len(conds) == 0 {
		return nil
	}
	windowStart := now.Add(-24 * time.Hour).UnixMilli()
	windowEnd := now.Add(24 * time.Hour).UnixMilli()
	start := 0
	for start < len(conds) && conds[start].UnixMs < windowStart {
		start++
	}
	if start >= len(conds) {
		start = 0
	}
	end := start
	for end < len(conds) && conds[end].UnixMs < windowEnd {
		end++
	}
	if end == start {
		end = min(start+24, len(conds))
	}
	candles := make([]ConditionCandle, 0, end-start)
	for _, condition := range conds[start:end] {
		candles = append(candles, ConditionCandle{
			Hour:      time.UnixMilli(condition.UnixMs).UTC().Format("15"),
			UnixMs:    condition.UnixMs,
			Score:     condition.Score,
			Condition: condition.Rating,
		})
	}
	return candles
}
