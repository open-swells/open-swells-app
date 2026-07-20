package main

// Hourly surf-condition classifier. Each forecast hour gets a 0-100 score:
// the energy of every swell train, filtered by how squarely it enters the
// beach's swell window, scaled by a wind quality factor (glassy or offshore
// up, onshore down). Scores bucket into poor / fair / good / epic.
//
// Directions follow the marine convention throughout: degrees true, the
// direction the swell or wind comes FROM. A beach's "facing" is the bearing
// toward open water, so a swell whose direction equals the facing hits the
// beach head-on, and a wind whose direction is opposite the facing blows
// offshore (land to sea).

import (
	"math"
	"time"
)

type SwellComponent struct {
	HeightM float64 // significant height, meters
	PeriodS float64
	DirDeg  float64 // direction the swell comes from, degrees true
}

// hourSwells is one forecast hour's swell trains, sampled for a spot.
type hourSwells struct {
	Time  time.Time
	Comps []SwellComponent
}

// HourlyCondition is one hour of the classified forecast, shaped for the
// spot page's condition strip (times as epoch ms for the client).
type HourlyCondition struct {
	UnixMs  int64   `json:"t"`
	Score   int     `json:"score"`
	Rating  string  `json:"rating"`
	FaceFt  float64 `json:"ft"` // exposure-weighted combined swell height, feet
	WindMph float64 `json:"wmph"`
	WindDir float64 `json:"wdir"` // direction wind comes from, degrees true
	HasWind bool    `json:"haswind"`
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

// swellExposure is how much of a train the beach receives, 0..1. A curated
// swell window is authoritative: full energy inside, tapering to zero 45
// degrees beyond its edges. Otherwise the estimated facing is used with a
// generous acceptance cone (refraction bends swell toward shore, so even
// oblique trains deliver some energy). With neither, everything passes.
func swellExposure(dir float64, facing float64, hasFacing bool, window []float64) float64 {
	if len(window) == 2 {
		width := math.Mod(window[1]-window[0]+360, 360)
		into := math.Mod(dir-window[0]+360, 360)
		if into <= width {
			return 1
		}
		past := math.Min(into-width, 360-into) // degrees beyond the nearer edge
		return math.Max(0, 1-past/45)
	}
	if !hasFacing {
		return 1
	}
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

// periodQuality weights a train by its period: short wind chop breaks weak
// and crumbly, long-period groundswell wraps in and jacks up on the bottom.
func periodQuality(p float64) float64 {
	switch {
	case p <= 5:
		return 0.5
	case p <= 9:
		return 0.5 + (p-5)/4*0.4 // -> 0.9
	case p <= 13:
		return 0.9 + (p-9)/4*0.25 // -> 1.15
	default:
		return math.Min(1.2, 1.15+(p-13)*0.0125)
	}
}

// effectiveFaceFt combines every train's exposure- and period-weighted
// height into one number, in feet. Root-sum-square addition mirrors how
// wave energy combines, so overlapping swells score above either alone.
func effectiveFaceFt(comps []SwellComponent, facing float64, hasFacing bool, window []float64) float64 {
	sum := 0.0
	for _, c := range comps {
		h := c.HeightM * swellExposure(c.DirDeg, facing, hasFacing, window) * periodQuality(c.PeriodS)
		sum += h * h
	}
	return math.Sqrt(sum) * 3.28084
}

// waveScore maps effective height to 0-100 with diminishing returns: the
// jump from flat to chest-high matters more than overhead to well-overhead.
func waveScore(ft float64) float64 {
	anchors := [][2]float64{{0, 0}, {1, 14}, {2, 34}, {3, 50}, {4, 62}, {6, 78}, {8, 87}, {10, 92}, {14, 97}}
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

// classifyHour scores one hour of swell + wind for a beach.
func classifyHour(comps []SwellComponent, facing float64, hasFacing bool, window []float64,
	windSpeedMs, windDir float64, hasWind bool) (score float64, faceFt float64) {

	faceFt = effectiveFaceFt(comps, facing, hasFacing, window)
	score = waveScore(faceFt)

	// Wind is judged against the facing; without one, the dominant swell's
	// direction stands in (swell comes from the sea, so wind opposing it
	// is offshore-ish — the user-visible "offshore effect").
	seaward, hasSeaward := facing, hasFacing
	if !hasSeaward {
		bestH := 0.0
		for _, c := range comps {
			if c.HeightM > bestH {
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
	return math.Min(100, math.Max(0, score)), faceFt
}

// conditionSeries classifies every forecast hour for a spot.
func conditionSeries(hours []hourSwells, wind []WindSample, windStart time.Time, hasWindStart bool,
	facing float64, hasFacing bool, window []float64) []HourlyCondition {

	out := make([]HourlyCondition, 0, len(hours))
	for _, h := range hours {
		var speedMs, dir float64
		hasWind := false
		if hasWindStart {
			speedMs, dir, hasWind = windAt(wind, windStart, h.Time)
		}
		score, faceFt := classifyHour(h.Comps, facing, hasFacing, window, speedMs, dir, hasWind)
		out = append(out, HourlyCondition{
			UnixMs:  h.Time.UnixMilli(),
			Score:   int(math.Round(score)),
			Rating:  ratingFor(score),
			FaceFt:  math.Round(faceFt*10) / 10,
			WindMph: math.Round(speedMs * 2.23694),
			WindDir: dir,
			HasWind: hasWind,
		})
	}
	return out
}

// applyConditionSummary upgrades the daily summary's height-only condition
// to the classifier's view of each day: a day is as good as its best hour.
func applyConditionSummary(summary []ForecastSummary, conds []HourlyCondition) {
	best := map[string]int{}
	for _, c := range conds {
		day := time.UnixMilli(c.UnixMs).UTC().Format("2006-01-02")
		if c.Score > best[day] {
			best[day] = c.Score
		}
	}
	for i := range summary {
		if score, ok := best[summary[i].Date]; ok {
			summary[i].Condition = ratingFor(float64(score))
			summary[i].Score = score
		}
	}
}

// currentConditionCandles picks the next 24 hourly entries from the
// classifier series, starting at the current hour, so the favorites strip
// is always a dense day-long window regardless of UTC day boundaries.
// A run that predates now entirely falls back to the series start.
func currentConditionCandles(conds []HourlyCondition) []ConditionCandle {
	if len(conds) == 0 {
		return nil
	}
	cutoff := time.Now().Add(-time.Hour).UnixMilli()
	start := 0
	for start < len(conds) && conds[start].UnixMs < cutoff {
		start++
	}
	if start >= len(conds) {
		start = 0
	}
	end := min(start+24, len(conds))
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
