package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"
)

const forecastCacheWarmEvery = time.Minute

type forecastCacheWarmResult struct {
	swell, wind, nwps, spots int
	elapsed                  time.Duration
}

// runForecastCacheWarmer moves grid decoding out of the first spot request.
// Rechecking every minute also warms newly rsynced model files; unchanged
// files are only statted because the loaders cache by mtime.
func runForecastCacheWarmer(ctx context.Context, forecastDir string) {
	result := warmForecastCachesOnce(forecastDir)
	log.Printf(
		"forecast cache warm: %d swell, %d wind, %d nearshore grids, %d spot series in %s",
		result.swell, result.wind, result.nwps, result.spots, result.elapsed.Round(time.Millisecond),
	)

	ticker := time.NewTicker(forecastCacheWarmEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result := warmForecastCachesOnce(forecastDir)
			if result.elapsed >= time.Second {
				log.Printf(
					"forecast cache refresh: %d swell, %d wind, %d nearshore grids, %d spot series in %s",
					result.swell, result.wind, result.nwps, result.spots, result.elapsed.Round(time.Millisecond),
				)
			}
		}
	}
}

func warmForecastCachesOnce(forecastDir string) forecastCacheWarmResult {
	started := time.Now()
	var result forecastCacheWarmResult
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		if set, err := loadSpotForecasts(forecastDir); err == nil {
			result.spots = len(set.Spots)
		}
	}()

	go func() {
		defer wg.Done()
		result.swell = warmGrid(runBundlePath(forecastDir, "swell"), mapHours(), func(hour int) error {
			_, err := swellGridPoints(filepath.Join(forecastDir, "swell_partitions_"+forecastHour(hour)+".geojson"))
			return err
		})
	}()

	go func() {
		defer wg.Done()
		result.wind = warmGrid(runBundlePath(forecastDir, "wind"), mapHours(), func(hour int) error {
			_, err := windGridPoints(filepath.Join(forecastDir, "wind_"+forecastHour(hour)+".geojson"))
			return err
		})
	}()

	go func() {
		defer wg.Done()
		for _, domain := range nwpsPointDomains(forecastDir) {
			bundle := ""
			if domain.File != "" {
				bundle = filepath.Join(forecastDir, domain.File)
			}
			result.nwps += warmGrid(bundle, domain.Hours, func(hour int) error {
				_, err := nwpsGridPoints(filepath.Join(
					forecastDir,
					"nwps_points_"+domain.WFO+"_"+domain.Grid+"_"+forecastHour(hour)+".geojson",
				))
				return err
			})
		}
	}()

	wg.Wait()
	result.elapsed = time.Since(started)
	return result
}

// mapHours are the 3-hourly forecast hours the server samples the coarse
// swell and wind grids at.
func mapHours() []int {
	var hours []int
	for hour := 0; hour <= windMaxHour; hour += 3 {
		hours = append(hours, hour)
	}
	return hours
}

// warmGrid decodes the given hours of a grid bundle, or for runs from
// before the bundles (path "") loads the per-hour GeoJSON through legacy.
// Returns how many hours are ready.
func warmGrid(path string, hours []int, legacy func(hour int) error) int {
	if path == "" {
		warmed := 0
		for _, hour := range hours {
			if legacy(hour) == nil {
				warmed++
			}
		}
		return warmed
	}
	b, err := loadGridBundle(path)
	if err != nil {
		log.Printf("forecast cache: %v", err)
		return 0
	}
	warmed := 0
	for _, hour := range hours {
		if !b.hasHour(hour) {
			continue
		}
		if _, err := b.frame(hour); err != nil {
			log.Printf("forecast cache: %v", err)
			continue
		}
		warmed++
	}
	return warmed
}

func forecastHour(hour int) string {
	return fmt.Sprintf("%03d", hour)
}
