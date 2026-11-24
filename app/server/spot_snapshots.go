package main

// Spot snapshots: the current hour and the week ahead for every spot,
// rebuilt in the background each hour and on each new run. Region pages
// list dozens to hundreds of spots and a spot's full forecast takes ~10ms
// to build, so they read these instead of building forecasts per request.

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"
)

// SpotSnapshot is one spot's conditions now and its next days.
type SpotSnapshot struct {
	Surf     string // breaking surf range now, e.g. "3–4ft"
	SurfHiFt float64
	Rating   string
	Score    int
	// The primary swell (feet, seconds, degrees true) and wind (mph,
	// degrees true) now; empty when unknown.
	SwellFt, SwellPeriod, SwellDir string
	WindMph, WindDir               string
	// Days are the spot's local days from today, at most snapshotDays.
	Days []ForecastSummary
}

const snapshotDays = 7

// HasNow is whether the snapshot has a rated current hour.
func (s SpotSnapshot) HasNow() bool { return s.Rating != "" }

func buildSpotSnapshot(staticDir string, spot Spot, now time.Time) (SpotSnapshot, bool) {
	forecastData, hours := spotForecast(staticDir, spot)
	if len(forecastData.Forecast) == 0 {
		return SpotSnapshot{}, false
	}
	var snap SpotSnapshot
	conditions := spotConditions(staticDir, spot, hours)
	if report, ok := spotReport(staticDir, spot, forecastData); ok {
		if c, ok := conditionNear(conditions, report.ValidUnixMs); ok {
			snap.Surf, snap.SurfHiFt, snap.Rating, snap.Score = c.Surf, c.SurfHiFt, c.Rating, c.Score
			// Quote the wind the rating was computed with.
			if c.HasWind {
				snap.WindMph, snap.WindDir = fmt.Sprintf("%.0f", c.WindMph), fmt.Sprintf("%.0f", c.WindDir)
			}
		}
		if !report.PrimaryIsWindSea {
			snap.SwellFt, snap.SwellPeriod, snap.SwellDir = report.PrimaryHeight, report.PrimaryPeriod, report.PrimaryDegrees
		}
	}
	snap.Days = upcomingDays(forecastData, conditions, spotLocation(spot), now)
	return snap, true
}

// upcomingDays are the spot's local days from today through the next
// snapshotDays calendar days, each rated by its best hour from now on: a
// morning that has passed doesn't make today the week's best day.
func upcomingDays(forecastData ForecastData, conditions []HourlyCondition, loc *time.Location, now time.Time) []ForecastSummary {
	summary := generateForecastSummary(forecastData, loc)
	from := now.Truncate(time.Hour).UnixMilli()
	ahead := conditionsFromRun(conditions, forecastData)
	for len(ahead) > 0 && ahead[0].UnixMs < from {
		ahead = ahead[1:]
	}
	applyConditionSummary(summary, ahead, loc)
	local := now.In(loc)
	today := local.Format("2006-01-02")
	last := local.AddDate(0, 0, snapshotDays-1).Format("2006-01-02")
	var days []ForecastSummary
	for _, day := range summary {
		if day.Date >= today && day.Date <= last {
			days = append(days, day)
		}
	}
	return days
}

// SnapshotSet is one complete build of every spot's snapshot. A page reads
// a single set, so its parts never mix two builds.
type SnapshotSet map[string]SpotSnapshot

func (s SnapshotSet) Get(id string) (SpotSnapshot, bool) {
	snap, ok := s[id]
	return snap, ok
}

type spotSnapshotStore struct {
	mu      sync.RWMutex
	key     string // run and hour the snapshots were built for
	builtAt time.Time
	data    SnapshotSet
}

var spotSnapshots = &spotSnapshotStore{}

// snapshotMaxAge is how long a build serves before it counts as stale: a
// warmer that stops rebuilding (no forecast files) must not leave pages
// showing yesterday's surf as "now".
const snapshotMaxAge = 3 * time.Hour

// Current is the latest build, nil before the first or once stale.
func (s *spotSnapshotStore) Current() SnapshotSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data == nil || time.Since(s.builtAt) > snapshotMaxAge {
		return nil
	}
	return s.data
}

func (s *spotSnapshotStore) Get(id string) (SpotSnapshot, bool) {
	return s.Current().Get(id)
}

func snapshotKey(staticDir string, now time.Time) string {
	return windForecastStart(staticDir) + "@" + now.UTC().Truncate(time.Hour).Format(time.RFC3339)
}

// refresh rebuilds every spot's snapshot when the run or the hour has
// changed since the last build. The old set serves until the new one is
// complete.
func (s *spotSnapshotStore) refresh(staticDir string, spots []Spot, now time.Time) bool {
	key := snapshotKey(staticDir, now)
	s.mu.RLock()
	current := s.key
	s.mu.RUnlock()
	if key == current || windForecastStart(staticDir) == "" {
		return false
	}
	data := make(SnapshotSet, len(spots))
	for _, spot := range spots {
		if snap, ok := buildSpotSnapshot(staticDir, spot, now); ok {
			data[spot.ID] = snap
		}
	}
	s.mu.Lock()
	s.key, s.data, s.builtAt = key, data, now
	s.mu.Unlock()
	return true
}

// runSpotSnapshotWarmer keeps the snapshots current. A full build takes
// under a minute of one core, once an hour.
func runSpotSnapshotWarmer(ctx context.Context, staticDir string, spots []Spot) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		start := time.Now()
		if spotSnapshots.refresh(staticDir, spots, start) {
			log.Printf("spot snapshots: %d spots in %v", len(spots), time.Since(start).Round(time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// --- Shared wording --------------------------------------------------------

var compassPoints = []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE", "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}

// compassPoint is the 16-point name for a bearing, e.g. "WNW".
func compassPoint(deg float64) string {
	return compassPoints[int((deg+360+11.25)/22.5)%16]
}

// compassString is compassPoint for a model direction string, "" when it
// is not a number.
func compassString(deg string) string {
	d, err := strconv.ParseFloat(deg, 64)
	if err != nil {
		return ""
	}
	return compassPoint(d)
}

// swellPhrase is e.g. "4.2ft at 14s from the WNW"; "" without a height.
func (s SpotSnapshot) swellPhrase() string {
	if s.SwellFt == "" {
		return ""
	}
	phrase := s.SwellFt + "ft"
	if p, err := strconv.ParseFloat(s.SwellPeriod, 64); err == nil {
		phrase += fmt.Sprintf(" at %.0fs", p)
	}
	if dir := compassString(s.SwellDir); dir != "" {
		phrase += " from the " + dir
	}
	return phrase
}

// surfFeet spaces the surf label's unit for prose: "3–4ft" -> "3–4 ft".
func surfFeet(label string) string {
	if n := len(label); n > 2 && label[n-2:] == "ft" {
		return label[:n-2] + " ft"
	}
	return label
}
