package main

// Station wind forecast, extracted from the wind bundle the pipeline
// writes into the static dir. Each request finds the nearest grid point to
// the station for every forecast hour; decoded hours are cached until the
// file changes, so lookups are cheap after the first hit per run.

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const windMaxHour = 384 // matches the 129-frame, 3-hourly forecast horizon

type windPoint struct {
	Lon, Lat float64
	S, D     float64
}

type WindSample struct {
	Hour  int     `json:"h"`
	Speed float64 `json:"s"` // m/s
	Dir   float64 `json:"d"` // direction wind comes from, degrees true
}

// windNearest is the wind grid's nearest valid cell within ~3 deg of
// (lat, lon) at a forecast hour; the error means the run has no such hour.
func windNearest(staticDir string, hour int, lat, lon float64) (windPoint, bool, error) {
	b, err := loadRunBundle(staticDir, "wind")
	if err != nil {
		return windPoint{}, false, err
	}
	f, err := b.frame(hour)
	if err != nil {
		return windPoint{}, false, err
	}
	vs, vd := b.varIndex("s"), b.varIndex("d")
	for _, c := range b.nearbyCells(lat, lon, 9) {
		s, okS := f.value(vs, c.Index)
		d, okD := f.value(vd, c.Index)
		if okS && okD {
			return windPoint{Lon: c.Lon, Lat: c.Lat, S: s, D: d}, true, nil
		}
	}
	return windPoint{}, false, nil
}

func windSeriesFor(staticDir string, lat, lon float64) []WindSample {
	samples := []WindSample{}
	for hour := 0; hour <= windMaxHour; hour += 3 {
		p, ok, err := windNearest(staticDir, hour, lat, lon)
		if err != nil {
			continue // hour not generated (partial run) — skip it
		}
		if ok {
			samples = append(samples, WindSample{Hour: hour, Speed: p.S, Dir: p.D})
		}
	}
	return samples
}

// windForecastStart reads forecast_start from the static run's metadata so
// the client can place samples on the timeline of the run that produced
// them (the station bulletin can be a newer cycle than the static layers).
func windForecastStart(staticDir string) string {
	return currentRunBundles(staticDir).ForecastStart
}

func windForecastHandler(staticDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		stationId := c.Param("stationId")
		var samples []WindSample
		if loc, ok := stationStore.Location(stationId); ok {
			samples = windSeriesFor(staticDir, loc.Latitude, loc.Longitude)
		} else if spot, ok := spotStore.Get(stationId); ok {
			// Spot pages reuse the buoy forecast template, whose wind
			// lane fetches by the id it was rendered with.
			samples = spotWind(staticDir, spot)
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "Unknown station"})
			return
		}
		c.Header("Cache-Control", "public, max-age=1800")
		c.JSON(http.StatusOK, gin.H{
			"start":   windForecastStart(staticDir),
			"samples": samples,
		})
	}
}
