package main

import (
	"context"
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
		result.swell = warmRunBundle(forecastDir, "swell")
	}()

	go func() {
		defer wg.Done()
		result.wind = warmRunBundle(forecastDir, "wind")
	}()

	go func() {
		defer wg.Done()
		for _, domain := range nwpsPointDomains(forecastDir) {
			result.nwps += warmGrid(filepath.Join(forecastDir, domain.File), domain.Hours)
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

// warmRunBundle warms this run's bundle for a product at the map hours.
func warmRunBundle(forecastDir, product string) int {
	name := currentRunBundles(forecastDir).Bundles[product]
	if name == "" {
		return 0
	}
	return warmGrid(filepath.Join(forecastDir, name), mapHours())
}

// warmGrid decodes the given hours of a grid bundle and returns how many
// hours are ready.
func warmGrid(path string, hours []int) int {
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
