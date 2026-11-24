package main

// Generated spot text: a few paragraphs about each break, written from the
// data the page already has (where it faces, the swell it is open to,
// which wind is offshore, the surf now and the best day this week). It
// gives every spot page prose of its own, rather than ~6,000 pages that
// differ only in their numbers, and gives link previews a description that
// says what the surf is doing.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// SpotText is a spot page's generated copy.
type SpotText struct {
	About   []string // the break itself; changes only with the spot data
	Outlook []string // the surf now and this week
	// Summary is one or two sentences for link previews.
	Summary string
}

type spotTextInput struct {
	Spot      Spot
	Name      string // the page's name for the spot (SEO name)
	Region    *RegionNode
	Beach     beach
	Report    SpotReport
	HasReport bool
	Now       HourlyCondition
	HasNow    bool
	Days      []ForecastSummary // from today, local days (upcomingDays)
	Today     string            // the spot's local date, 2006-01-02
	Nearby    []NearbySpot
	HasTide   bool
	Loc       *time.Location
}

func buildSpotText(in spotTextInput) SpotText {
	var t SpotText
	t.About = spotAbout(in)
	if p := spotNowParagraph(in); p != "" {
		t.Outlook = append(t.Outlook, p)
	}
	if p := spotWeekParagraph(in); p != "" {
		t.Outlook = append(t.Outlook, p)
	}
	source := "Forecasts update every six hours from NOAA's GFS-Wave model"
	if in.HasReport && in.Report.SwellHeight != "" {
		source += ", with nearshore detail from the National Weather Service's NWPS model"
	}
	if in.HasTide {
		source += ", alongside tide predictions for the beach"
	}
	t.Outlook = append(t.Outlook, source+".")
	t.Summary = spotSummary(in)
	return t
}

func spotAbout(in spotTextInput) []string {
	var paras []string
	where := strings.TrimSpace(in.Spot.Region)
	if in.Region != nil {
		where = in.Region.ProseName()
	}
	first := in.Name + " is a surf spot"
	if where != "" {
		first += " in " + where
	}
	first += "."
	b := in.Beach
	if b.HasFacing {
		first += fmt.Sprintf(" The beach faces %s (%.0f°)", compassPoint(b.Facing), math.Mod(b.Facing+360, 360))
		if window := exposurePhrase(b); window != "" {
			first += ", " + window
		}
		first += "."
		// Only claims the classifier backs: a hand-tuned window treats
		// every direction inside it alike, and a facing just outside the
		// open water still takes swell (exposure tapers past the edge).
		if len(b.Window) != 2 {
			if facingBlocked(b) {
				first += " Land shelters it from swell straight out, so swell has to bend in from the open water."
			} else {
				first += fmt.Sprintf(" Swell from the %s meets it most squarely.", compassPoint(b.Facing))
			}
		}
		first += fmt.Sprintf(" Offshore wind here blows from the %s.", compassPoint(b.Facing+180))
	}
	paras = append(paras, first)

	if len(in.Nearby) > 0 {
		var names []string
		for i, n := range in.Nearby {
			if i == 3 {
				break
			}
			names = append(names, fmt.Sprintf("%s (%.1f mi)", n.Name, n.Distance))
		}
		if len(names) == 1 {
			paras = append(paras, "The closest other break with a forecast is "+names[0]+".")
		} else {
			paras = append(paras, "The closest other breaks with forecasts are "+joinList(names)+".")
		}
	}
	return paras
}

// facingBlocked is whether land blocks swell from straight out: the
// facing lies beyond every open-water arc by more than the taper that
// still lets swell in.
func facingBlocked(b beach) bool {
	if len(b.OpenWater) == 0 {
		return false
	}
	for _, arc := range b.OpenWater {
		if arcPast(b.Facing, arc) < openWaterTaperDeg {
			return false
		}
	}
	return true
}

// exposurePhrase describes the swell a beach is open to, e.g. "with a
// swell window from the SW (225°) round to the NW (315°)".
func exposurePhrase(b beach) string {
	if len(b.Window) == 2 {
		return fmt.Sprintf("with a swell window from the %s (%.0f°) round to the %s (%.0f°)",
			compassPoint(b.Window[0]), b.Window[0], compassPoint(b.Window[1]), b.Window[1])
	}
	if len(b.OpenWater) == 0 {
		return ""
	}
	widest, width := b.OpenWater[0], -1.0
	total := 0.0
	for _, arc := range b.OpenWater {
		w := math.Mod(arc[1]-arc[0]+360, 360)
		if w == 0 {
			w = 360
		}
		total += w
		if w > width {
			widest, width = arc, w
		}
	}
	switch {
	case total >= 300:
		return "open to swell from almost every direction"
	case len(b.OpenWater) > 1:
		return fmt.Sprintf("with its widest stretch of open water from the %s (%.0f°) round to the %s (%.0f°)",
			compassPoint(widest[0]), widest[0], compassPoint(widest[1]), widest[1])
	default:
		return fmt.Sprintf("open to swell from the %s (%.0f°) round to the %s (%.0f°)",
			compassPoint(widest[0]), widest[0], compassPoint(widest[1]), widest[1])
	}
}

// windRelation is how wind from windDir meets a beach facing facing.
func windRelation(windDir, facing float64) string {
	switch d := angDiff(windDir, facing); {
	case d <= 60:
		return "onshore"
	case d >= 120:
		return "offshore"
	default:
		return "cross-shore"
	}
}

func spotNowParagraph(in spotTextInput) string {
	if !in.HasReport {
		return ""
	}
	r := in.Report
	var s string
	if in.HasNow && in.Now.Surf != "" {
		s = fmt.Sprintf("Right now the forecast calls for %s surf, rated %s", surfFeet(in.Now.Surf), in.Now.Rating)
	} else {
		s = "Right now"
	}
	swell := reportSwellPhrase(r.PrimaryHeight, r.PrimaryPeriod, r.PrimaryDegrees)
	if swell != "" {
		kind := "swell"
		if r.PrimaryIsWindSea {
			kind = "wind swell"
		}
		if s == "Right now" {
			s += fmt.Sprintf(" the main %s is %s", kind, swell)
		} else {
			s += fmt.Sprintf(", from a %s %s", swell, kind)
		}
		if r.HasSecondary {
			if second := reportSwellPhrase(r.SecondaryHeight, r.SecondaryPeriod, r.SecondaryDegrees); second != "" {
				s += " and a secondary " + second + " swell"
			}
		}
	}
	if s == "Right now" {
		return ""
	}
	s += "."
	// The wind the rating was computed with, when there is a rated hour.
	speed, dir, hasWind := 0.0, 0.0, false
	if in.HasNow && in.Now.HasWind {
		speed, dir, hasWind = in.Now.WindMph, in.Now.WindDir, true
	} else if r.HasWind {
		s1, e1 := strconv.ParseFloat(r.WindSpeed, 64)
		d1, e2 := strconv.ParseFloat(r.WindDir, 64)
		speed, dir, hasWind = s1, d1, e1 == nil && e2 == nil
	}
	if hasWind {
		s += fmt.Sprintf(" The wind is %.0f mph from the %s", speed, compassPoint(dir))
		if in.Beach.HasFacing {
			s += ", " + windRelation(dir, in.Beach.Facing)
		}
		s += "."
	}
	return s
}

// reportSwellPhrase is e.g. "4.2ft, 14-second WNW"; "" without a height.
func reportSwellPhrase(heightFt, period, degrees string) string {
	if heightFt == "" {
		return ""
	}
	parts := []string{heightFt + "ft"}
	if p, err := strconv.ParseFloat(period, 64); err == nil {
		parts = append(parts, fmt.Sprintf("%.0f-second", p))
	}
	phrase := strings.Join(parts, ", ")
	if dir := compassString(degrees); dir != "" {
		phrase += " " + dir
	}
	return phrase
}

// bestDay is the highest-scoring day, the earliest on a tie.
func bestDay(days []ForecastSummary) (ForecastSummary, bool) {
	if len(days) == 0 {
		return ForecastSummary{}, false
	}
	best := days[0]
	for _, d := range days[1:] {
		if d.Score > best.Score {
			best = d
		}
	}
	return best, true
}

func dayName(date string, layout string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Format(layout)
}

func spotWeekParagraph(in spotTextInput) string {
	best, ok := bestDay(in.Days)
	if !ok {
		return ""
	}
	n := len(in.Days)
	rideable := 0
	for _, d := range in.Days {
		if d.Condition != "poor" {
			rideable++
		}
	}
	when := dayName(best.Date, "Monday, January 2")
	if best.Date == in.Today {
		when = "today"
	}
	span := fmt.Sprintf("the next %d days", n)
	if n == 1 {
		span = "the forecast's one remaining day"
	}
	if rideable == 0 {
		return fmt.Sprintf("Nothing in %s rates better than poor; the highest-rated day is %s, with %s.",
			span, when, surfFeet(best.WaveHeight))
	}
	s := fmt.Sprintf("Over %s the best surf looks to be %s, with %s rated %s.",
		span, when, surfFeet(best.WaveHeight), best.Condition)
	switch {
	case n == 1:
	case rideable == n:
		s += " Every one of those days rates fair or better."
	default:
		s += fmt.Sprintf(" %d of the %d rate fair or better.", rideable, n)
	}
	return s
}

func spotSummary(in spotTextInput) string {
	var parts []string
	if in.HasNow && in.Now.Surf != "" {
		parts = append(parts, fmt.Sprintf("%s and %s right now", surfFeet(in.Now.Surf), in.Now.Rating))
	}
	if best, ok := bestDay(in.Days); ok && best.Condition != "poor" && len(in.Days) > 1 {
		when := dayName(best.Date, "Mon")
		if best.Date == in.Today {
			when = "today"
		}
		parts = append(parts, fmt.Sprintf("best day %s with %s (%s)",
			when, surfFeet(best.WaveHeight), best.Condition))
	}
	tail := "Free 16-day surf forecast for " + in.Name + ": swell, wind and tide."
	if len(parts) == 0 {
		return tail
	}
	s := strings.Join(parts, "; ")
	return strings.ToUpper(s[:1]) + s[1:] + ". " + tail
}

func joinList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}
