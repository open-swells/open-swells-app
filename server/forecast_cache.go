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
	swell, wind, nwps int
	elapsed           time.Duration
}

// runForecastCacheWarmer moves the expensive GeoJSON decoding out of the
// first spot request. Rechecking every minute also warms newly rsynced model
// files; unchanged files are only statted because the loaders cache by mtime.
func runForecastCacheWarmer(ctx context.Context, forecastDir string) {
	result := warmForecastCachesOnce(forecastDir)
	log.Printf(
		"forecast cache warm: %d swell, %d wind, %d nearshore grids in %s",
		result.swell, result.wind, result.nwps, result.elapsed.Round(time.Millisecond),
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
					"forecast cache refresh: %d swell, %d wind, %d nearshore grids in %s",
					result.swell, result.wind, result.nwps, result.elapsed.Round(time.Millisecond),
				)
			}
		}
	}
}

func warmForecastCachesOnce(forecastDir string) forecastCacheWarmResult {
	started := time.Now()
	var result forecastCacheWarmResult
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for hour := 0; hour <= windMaxHour; hour += 3 {
			path := filepath.Join(forecastDir, "swell_partitions_"+forecastHour(hour)+".geojson")
			if _, err := swellGridPoints(path); err == nil {
				result.swell++
			}
		}
	}()

	go func() {
		defer wg.Done()
		for hour := 0; hour <= windMaxHour; hour += 3 {
			path := filepath.Join(forecastDir, "wind_"+forecastHour(hour)+".geojson")
			if _, err := windGridPoints(path); err == nil {
				result.wind++
			}
		}
	}()

	go func() {
		defer wg.Done()
		seen := make(map[string]struct{})
		for _, domain := range nwpsPointDomains(forecastDir) {
			for _, hour := range domain.Hours {
				path := filepath.Join(
					forecastDir,
					"nwps_points_"+domain.WFO+"_"+domain.Grid+"_"+forecastHour(hour)+".geojson",
				)
				if _, ok := seen[path]; ok {
					continue
				}
				seen[path] = struct{}{}
				if _, err := nwpsGridPoints(path); err == nil {
					result.nwps++
				}
			}
		}
	}()

	wg.Wait()
	result.elapsed = time.Since(started)
	return result
}

func forecastHour(hour int) string {
	return fmt.Sprintf("%03d", hour)
}
