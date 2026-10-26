package main

// The spot history keeps the hours around the latest run's start, so a new
// run (which starts at its cycle time) can be backfilled with the hours of
// today before it. It is one small file, rewritten once per run: every spot,
// hourly, from spotHistoryHours before the newest run's start to
// spotHistoryHours after it. Each update merges spot-hour by spot-hour,
// newest run first, so an hour a run failed (or a spot it lacked) keeps the
// earlier run's values instead of opening a gap. Since a run's own hours
// reach a day ahead, a run the server never loaded (it was down, or a newer
// run landed first) leaves no gap either: the hours come from the run
// before.
//
// The file sits beside the forecast dir rather than in it: the dir is
// served publicly, and the pipeline's publish owns its contents. Payload:
//
//	{"run": "20260720_06Z", "start": "20260719_06Z",
//	 "fields": ["hs", ...], "spots": {"<id>": [[...], ...]}}
//
// "start" is the valid time of index 0 and each spot holds one hourly
// series per field, null where no run had a value.

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	spotHistoryFile = "spot_forecast_history.json.gz"
	// A day either side of a run's start covers local midnight in every
	// time zone for as long as the run is current.
	spotHistoryHours  = 24
	forecastRunLayout = "20060102_15Z"
)

type spotHistory struct {
	Run    string                   `json:"run"`
	Start  string                   `json:"start"`
	Fields []string                 `json:"fields"`
	Spots  map[string][]floatSeries `json:"spots"`
}

var spotHistoryMu sync.Mutex

func spotHistoryPath(staticDir string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(staticDir)), spotHistoryFile)
}

// readSpotHistory returns nil when there is no usable history.
func readSpotHistory(staticDir string) *spotHistory {
	f, err := os.Open(spotHistoryPath(staticDir))
	if err != nil {
		return nil
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		log.Printf("spot history: %v", err)
		return nil
	}
	defer zr.Close()
	var h spotHistory
	if err := json.NewDecoder(zr).Decode(&h); err != nil {
		log.Printf("spot history: %v", err)
		return nil
	}
	if _, err := time.Parse(forecastRunLayout, h.Start); err != nil || !slices.Equal(h.Fields, spotSeriesFields) {
		log.Printf("spot history: unrecognized layout, ignoring it")
		return nil
	}
	return &h
}

// at is field f's value valid at hour index i of the history.
func (h *spotHistory) at(id string, f, i int) float64 {
	series := h.Spots[id]
	if f >= len(series) {
		return math.NaN()
	}
	return series[f].at(i)
}

// backfillSpotForecasts prefixes set with the history's spotHistoryHours
// hours before the run's start, as negative forecast hours. The values sit
// in each series' pre rather than being prepended, which would copy the
// whole run.
func backfillSpotForecasts(set *spotForecastSet, h *spotHistory) {
	if h == nil || len(set.Hours) == 0 || set.Hours[0] != 0 {
		return
	}
	start, err := time.Parse(forecastRunLayout, set.ForecastStart)
	if err != nil {
		return
	}
	histStart, _ := time.Parse(forecastRunLayout, h.Start)
	const n = spotHistoryHours
	offset := int(start.Sub(histStart).Hours()) - n // history index of hour -n

	missing := &spotSeries{HS: nanSeries(n)}
	found := false
	pres := make(map[string]*spotSeries, len(set.Spots))
	for id := range set.Spots {
		if _, ok := h.Spots[id]; !ok {
			pres[id] = missing
			continue
		}
		pre := &spotSeries{}
		for f, field := range pre.fields() {
			values := make(floatSeries, n)
			for k := range values {
				v := h.at(id, f, offset+k)
				values[k] = float32(v)
				found = found || !math.IsNaN(v)
			}
			*field = values
		}
		pres[id] = pre
	}
	if !found {
		return
	}
	for id, series := range set.Spots {
		series.pre = pres[id]
	}
	hours := make([]int, 0, n+len(set.Hours))
	for k := -n; k < 0; k++ {
		hours = append(hours, k)
	}
	set.Hours = append(hours, set.Hours...)
}

func nanSeries(n int) floatSeries {
	s := make(floatSeries, n)
	for i := range s {
		s[i] = float32(math.NaN())
	}
	return s
}

// updateSpotHistory folds set into the history and rewrites it, when set
// is newer than the run the history last took in. The window moves to
// center on set's start; set's values win wherever it has them.
func updateSpotHistory(staticDir string, set *spotForecastSet, h *spotHistory) {
	start, err := time.Parse(forecastRunLayout, set.ForecastStart)
	if err != nil {
		return
	}
	spotHistoryMu.Lock()
	defer spotHistoryMu.Unlock()
	if h != nil {
		if last, err := time.Parse(forecastRunLayout, h.Run); err == nil && !start.After(last) {
			return
		}
	}

	const n = spotHistoryHours
	windowStart := start.Add(-n * time.Hour)
	shift := 0
	if h != nil {
		histStart, _ := time.Parse(forecastRunLayout, h.Start)
		shift = int(windowStart.Sub(histStart).Hours())
	}
	next := &spotHistory{
		Run:    set.ForecastStart,
		Start:  windowStart.Format(forecastRunLayout),
		Fields: spotSeriesFields,
		Spots:  make(map[string][]floatSeries, len(set.Spots)),
	}
	for id, series := range set.Spots {
		out := make([]floatSeries, len(spotSeriesFields))
		for f := range out {
			out[f] = make(floatSeries, 2*n)
			for k := range out[f] {
				v := math.NaN()
				if h != nil {
					v = h.at(id, f, shift+k)
				}
				out[f][k] = float32(v)
			}
		}
		for i, hour := range set.Hours {
			k := hour + n
			if k < 0 || k >= 2*n {
				continue
			}
			// A spot-hour is taken whole or not at all: within an hour a
			// missing partition is real, not a hole to fill from older runs.
			src, j := series.locate(i)
			if math.IsNaN(src.HS.at(j)) {
				continue
			}
			for f, field := range src.fields() {
				out[f][k] = float32(field.at(j))
			}
		}
		next.Spots[id] = out
	}
	if err := writeSpotHistory(spotHistoryPath(staticDir), next); err != nil {
		log.Printf("spot history: %v", err)
	}
}

// writeSpotHistory replaces the file atomically, streaming the encode so
// the JSON is never held in memory whole.
func writeSpotHistory(path string, h *spotHistory) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spot-history-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	buf := bufio.NewWriter(tmp)
	zw, _ := gzip.NewWriterLevel(buf, gzip.BestSpeed)
	if err := json.NewEncoder(zw).Encode(h); err != nil {
		tmp.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := buf.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
