package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BuoyReport is a buoy's latest observations in the units and shape of the
// spot report (SpotReport): feet, mph, and directions in degrees true, so
// both render with the same "now" design.
type BuoyReport struct {
	ObservedUnixMs int64
	Observed       string // UTC fallback when JavaScript is unavailable
	// Significant wave height of all seas, feet.
	Seas    string
	HasSeas bool
	// Swell and local wind sea, feet / seconds / degrees true.
	SwellHeight, SwellPeriod, SwellDegrees       string
	HasSwell                                     bool
	WindSeaHeight, WindSeaPeriod, WindSeaDegrees string
	HasWindSea                                   bool
	// NDBC's wave steepness as a phrase: "swell-dominated", "average
	// seas", "very steep seas"; empty when unreported.
	Steepness                    string
	WindSpeed, WindGust, WindDir string // mph, mph, degrees true
	HasWind                      bool
	AirTemp, WaterTemp           string // °C
}

func buoyReport(swell SwellReport, wind WindReport) BuoyReport {
	var r BuoyReport
	if t, err := time.Parse("2006/01/02 15:04", swell.Date); err == nil {
		r.ObservedUnixMs = t.UnixMilli()
		r.Observed = t.Format("Jan 2 15:04 UTC")
	}
	feet := func(meters string) string {
		m, err := strconv.ParseFloat(meters, 64)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%.1f", m*3.28084)
	}
	mph := func(ms string) string {
		v, err := strconv.ParseFloat(ms, 64)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%.0f", v*2.23694)
	}

	r.Seas = feet(swell.WaveHeight)
	r.HasSeas = r.Seas != ""
	r.SwellHeight, r.SwellPeriod, r.SwellDegrees = feet(swell.SwellHeight), swell.SwellPeriod, swell.SwellDegrees
	r.HasSwell = r.SwellHeight != ""
	r.WindSeaHeight, r.WindSeaPeriod, r.WindSeaDegrees = feet(swell.WindSeaHeight), swell.WindSeaPeriod, swell.WindSeaDegrees
	r.HasWindSea = r.WindSeaHeight != ""
	switch steep := strings.ToLower(strings.ReplaceAll(swell.Steepness, "_", " ")); steep {
	case "", "n/a":
	case "swell":
		r.Steepness = "swell-dominated"
	default:
		r.Steepness = steep + " seas"
	}

	r.WindSpeed, r.WindGust, r.WindDir = mph(wind.WindSpeed), mph(wind.WindGust), wind.WindDir
	r.HasWind = r.WindSpeed != ""
	r.AirTemp, r.WaterTemp = wind.AirTemp, wind.WaterTemp
	return r
}
