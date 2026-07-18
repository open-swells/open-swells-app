package main

// Station wind forecast, extracted from the wind_XXX.geojson grids the
// pipeline writes into the static dir. Each request finds the nearest grid
// point to the station for every forecast hour; parsed grids are cached by
// file mtime so the scan is cheap after the first hit per run.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gin-gonic/gin"
)

const windMaxHour = 384 // matches the 129-frame, 3-hourly forecast horizon

type windPoint struct {
	Lon, Lat float64
	S, D     float64
}

type windGridEntry struct {
	modTime int64
	points  []windPoint
}

var (
	windGridMu    sync.Mutex
	windGridCache = map[string]windGridEntry{}
)

type WindSample struct {
	Hour  int     `json:"h"`
	Speed float64 `json:"s"` // m/s
	Dir   float64 `json:"d"` // direction wind comes from, degrees true
}

func windGridPoints(path string) ([]windPoint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	windGridMu.Lock()
	defer windGridMu.Unlock()
	cached, ok := windGridCache[path]
	if ok && cached.modTime == info.ModTime().UnixNano() {
		return cached.points, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Features []struct {
			Geometry struct {
				Coordinates [2]float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties struct {
				S float64 `json:"s"`
				D float64 `json:"d"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	points := make([]windPoint, 0, len(doc.Features))
	for _, f := range doc.Features {
		points = append(points, windPoint{
			Lon: f.Geometry.Coordinates[0],
			Lat: f.Geometry.Coordinates[1],
			S:   f.Properties.S,
			D:   f.Properties.D,
		})
	}

	windGridCache[path] = windGridEntry{modTime: info.ModTime().UnixNano(), points: points}
	return points, nil
}

// nearestWind picks the grid point closest to (lat, lon), tolerant of the
// grid using 0..360 longitudes while stations use -180..180.
func nearestWind(points []windPoint, lat, lon float64) (windPoint, bool) {
	best := windPoint{}
	bestDist := math.MaxFloat64
	for _, p := range points {
		dlng := math.Abs(p.Lon - lon)
		if dlng > 180 {
			dlng = 360 - dlng
		}
		dist := dlng*dlng + (p.Lat-lat)*(p.Lat-lat)
		if dist < bestDist {
			bestDist = dist
			best = p
		}
	}
	// A grid point more than ~3 degrees away is not this station's wind.
	return best, bestDist <= 9
}

func windSeriesFor(staticDir string, lat, lon float64) []WindSample {
	samples := []WindSample{}
	for hour := 0; hour <= windMaxHour; hour += 3 {
		path := filepath.Join(staticDir, fmt.Sprintf("wind_%03d.geojson", hour))
		points, err := windGridPoints(path)
		if err != nil {
			continue // hour not generated (partial run) — skip it
		}
		if p, ok := nearestWind(points, lat, lon); ok {
			samples = append(samples, WindSample{Hour: hour, Speed: p.S, Dir: p.D})
		}
	}
	return samples
}

// windForecastStart reads forecast_start from the static run's metadata so
// the client can place samples on the timeline of the run that produced
// them (the station bulletin can be a newer cycle than the static layers).
func windForecastStart(staticDir string) string {
	raw, err := os.ReadFile(filepath.Join(staticDir, "metadata.json"))
	if err != nil {
		return ""
	}
	var meta struct {
		ForecastStart string `json:"forecast_start"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return ""
	}
	return meta.ForecastStart
}

func windForecastHandler(staticDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		stationId := c.Param("stationId")
		var lat, lon float64
		if loc, ok := stationStore.Location(stationId); ok {
			lat, lon = loc.Latitude, loc.Longitude
		} else if spot, ok := spotStore.Get(stationId); ok {
			// Spot pages reuse the buoy forecast template, whose wind
			// lane fetches by the id it was rendered with.
			lat, lon = spot.samplePoint()
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "Unknown station"})
			return
		}
		c.Header("Cache-Control", "public, max-age=1800")
		c.JSON(http.StatusOK, gin.H{
			"start":   windForecastStart(staticDir),
			"samples": windSeriesFor(staticDir, lat, lon),
		})
	}
}
