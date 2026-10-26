package main

// The landing page's live report: the spot page's report and forecast for
// a well-known break that has rideable surf right now, so a visitor lands
// on a real report instead of a description of one. ?spot=<id> features a
// specific candidate instead.
//
// Picking the spot means classifying every candidate's current hour, so
// the ranking is cached per forecast run and refreshed every few minutes
// as "now" moves through the run; each candidate's page data is cached the
// same way.

import (
	"math"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// landingCandidateIDs are the breaks the landing page may feature: famous
// spots spread across coasts and time zones, so some are usually working.
// IDs missing from spots.json are skipped.
var landingCandidateIDs = []string{
	"trestles", "huntington-pier", "swamis", "malibu-first-point",
	"rincon-usa-california-santa-barbara", "steamer-lane", "pleasure-point",
	"short-sands", "pipeline", "waimea-bay",
	"cocoa-beach-pier", "sebastian-inlet", "folly-beach-pier",
	"hatteras-light-house", "manasquan-inlet", "montauk-point-turtles",
	"snapper-rocks", "bells-beach", "supertubos", "praia-da-nazare",
	"mundaka", "puerto-escondido", "uluwatu", "cloudbreak", "teahupoo",
	"raglan-manu-bay",
}

const landingTTL = 10 * time.Minute

// LandingSpot is one candidate as ranked right now.
type LandingSpot struct {
	ID, Name, Region string
	Rating, Surf     string // the classified current hour
	Score            int
	SurfHiFt         float64
	HasTide          bool
	Daylight         bool // the sun is up (or near enough) at the spot
}

// Rideable is surf worth paddling out for: a rating above poor and at
// least waist-high sets. Daylight is ranked separately, so a dark coast
// still offers its spots when nowhere else is working.
func (s LandingSpot) Rideable() bool {
	return s.Rating != "" && s.Rating != "poor" && s.SurfHiFt >= 2
}

// sunElevation is the sun's approximate elevation in degrees at a point
// and time (NOAA's low-precision solar position: good to a degree or so,
// plenty for "is it light out").
func sunElevation(lat, lon float64, t time.Time) float64 {
	t = t.UTC()
	rad := math.Pi / 180
	day := float64(t.YearDay())
	hour := float64(t.Hour()) + float64(t.Minute())/60
	g := 2 * math.Pi / 365 * (day - 1 + (hour-12)/24) // fractional year
	decl := 0.006918 - 0.399912*math.Cos(g) + 0.070257*math.Sin(g) -
		0.006758*math.Cos(2*g) + 0.000907*math.Sin(2*g) -
		0.002697*math.Cos(3*g) + 0.00148*math.Sin(3*g)
	eqTime := 229.18 * (0.000075 + 0.001868*math.Cos(g) - 0.032077*math.Sin(g) -
		0.014615*math.Cos(2*g) - 0.040849*math.Sin(2*g)) // minutes
	solarMinutes := hour*60 + eqTime + 4*lon
	hourAngle := (solarMinutes/4 - 180) * rad
	sinElev := math.Sin(lat*rad)*math.Sin(decl) + math.Cos(lat*rad)*math.Cos(decl)*math.Cos(hourAngle)
	return math.Asin(sinElev) / rad
}

// rankLandingSpot classifies a candidate's current hour.
func rankLandingSpot(staticDir string, spot Spot, now time.Time) (LandingSpot, bool) {
	forecastData, hours := spotForecast(staticDir, spot)
	if len(forecastData.Forecast) == 0 {
		return LandingSpot{}, false
	}
	report, ok := spotReport(staticDir, spot, forecastData)
	if !ok {
		return LandingSpot{}, false
	}
	c, ok := conditionNear(spotConditions(staticDir, spot, hours), report.ValidUnixMs)
	if !ok {
		return LandingSpot{}, false
	}
	rows := forecastData.Forecast
	_, hasTide := spotTide(staticDir, spot.ID, rows[0].Time, rows[len(rows)-1].Time)
	return LandingSpot{
		ID: spot.ID, Name: spot.Name, Region: spot.Region,
		Rating: c.Rating, Surf: c.Surf, Score: c.Score, SurfHiFt: c.SurfHiFt,
		HasTide:  hasTide,
		Daylight: sunElevation(spot.Lat, spot.Lon, now) > -6, // civil twilight
	}, true
}

// rankLandingSpots orders candidates best first: rideable spots before
// the rest, then spots where it is light enough to paddle out, then spots
// with a tide station (the report's tide lane is half of what a surfer
// checks), then by the classifier's score.
func rankLandingSpots(spots []LandingSpot) {
	sort.SliceStable(spots, func(i, j int) bool {
		a, b := spots[i], spots[j]
		if a.Rideable() != b.Rideable() {
			return a.Rideable()
		}
		if a.Daylight != b.Daylight {
			return a.Daylight
		}
		if a.HasTide != b.HasTide {
			return a.HasTide
		}
		return a.Score > b.Score
	})
}

// LandingReport is the landing page's live report: the featured spot's
// full spot page data.
type LandingReport struct {
	Spot    SpotPageData
	ID      string
	HasSpot bool
}

type landingCache struct {
	mu     sync.Mutex
	run    string
	built  time.Time
	ranked []LandingSpot
	pages  map[string]SpotPageData
	group  singleflight.Group
}

var landing landingCache

// report returns the live report for the requested candidate, or for the
// best-ranked one when id is empty or not a candidate.
func (lc *landingCache) report(staticDir string, store *SpotStore, zones *SurfZoneStore, siteURL, id string) LandingReport {
	ranked := lc.ranking(staticDir, store)
	if len(ranked) == 0 {
		return LandingReport{}
	}
	pick := ranked[0]
	for _, s := range ranked {
		if s.ID == id {
			pick = s
			break
		}
	}
	spot, ok := store.Get(pick.ID)
	if !ok {
		return LandingReport{}
	}
	page := lc.page(staticDir, zones, siteURL, spot)
	return LandingReport{Spot: page, ID: pick.ID, HasSpot: page.HasForecast}
}

// ranking returns the candidates best first, rebuilt when the forecast run
// changes or the ranking is older than landingTTL.
func (lc *landingCache) ranking(staticDir string, store *SpotStore) []LandingSpot {
	run := currentRunID(staticDir)
	lc.mu.Lock()
	if lc.run == run && time.Since(lc.built) < landingTTL {
		ranked := lc.ranked
		lc.mu.Unlock()
		return ranked
	}
	lc.mu.Unlock()

	v, _, _ := lc.group.Do("rank", func() (interface{}, error) {
		var ranked []LandingSpot
		now := time.Now()
		for _, id := range landingCandidateIDs {
			if spot, ok := store.Get(id); ok {
				if s, ok := rankLandingSpot(staticDir, spot, now); ok {
					ranked = append(ranked, s)
				}
			}
		}
		rankLandingSpots(ranked)
		lc.mu.Lock()
		lc.run, lc.built, lc.ranked = run, time.Now(), ranked
		lc.pages = map[string]SpotPageData{} // "now" moved; rebuild pages too
		lc.mu.Unlock()
		return ranked, nil
	})
	return v.([]LandingSpot)
}

// page is the candidate's spot page data, cached alongside the ranking.
func (lc *landingCache) page(staticDir string, zones *SurfZoneStore, siteURL string, spot Spot) SpotPageData {
	lc.mu.Lock()
	page, ok := lc.pages[spot.ID]
	lc.mu.Unlock()
	if ok {
		return page
	}
	v, _, _ := lc.group.Do("page:"+spot.ID, func() (interface{}, error) {
		page := spotPageData(staticDir, zones, siteURL, spot)
		lc.mu.Lock()
		if lc.pages != nil {
			lc.pages[spot.ID] = page
		}
		lc.mu.Unlock()
		return page, nil
	})
	return v.(SpotPageData)
}
