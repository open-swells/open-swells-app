package main

// Spot tides from spot_tides.json.gz: the pipeline matches each spot to its
// nearest NOAA CO-OPS prediction station and stores that station's high/low
// events (see grib-parse-collect/tides.py). Most coastal stations are
// subordinate stations, which CO-OPS only predicts as highs and lows, so the
// spot page draws the curve between events itself. Reference stations also
// carry hourly predictions, which the curve follows instead.
//
// Predictions are astronomical and don't belong to a model run, so unlike
// spot_forecasts.json.gz the file isn't checked against metadata.json; the
// page only uses events around its forecast window.

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const spotTidesFile = "spot_tides.json.gz"

type tideStation struct {
	Name string      `json:"name"`
	Lat  float64     `json:"lat"`
	Lon  float64     `json:"lon"`
	Type string      `json:"type"` // "R" reference (harmonic) or "S" subordinate
	HiLo []TideEvent `json:"hilo"`
	// Reference stations only: meters MLLW hourly from Start (unix seconds).
	Hourly *struct {
		Start int64     `json:"start"`
		V     []float64 `json:"v"`
	} `json:"hourly"`
}

type spotTideSet struct {
	Stations map[string]*tideStation `json:"stations"`
	Spots    map[string]struct {
		Station string  `json:"station"`
		Km      float64 `json:"km"`
	} `json:"spots"`
}

// TideEvent is one predicted high or low: T epoch ms, V meters MLLW.
type TideEvent struct {
	T    int64   `json:"t"`
	V    float64 `json:"v"`
	High bool    `json:"high"`
}

// UnmarshalJSON reads the file's compact [unix seconds, meters, "H" | "L"].
// (Marshaling keeps the named fields for the page.)
func (e *TideEvent) UnmarshalJSON(data []byte) error {
	var raw [3]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("tide event: %w", err)
	}
	var (
		seconds int64
		kind    string
	)
	if err := json.Unmarshal(raw[0], &seconds); err != nil {
		return fmt.Errorf("tide event time: %w", err)
	}
	if err := json.Unmarshal(raw[1], &e.V); err != nil {
		return fmt.Errorf("tide event height: %w", err)
	}
	if err := json.Unmarshal(raw[2], &kind); err != nil {
		return fmt.Errorf("tide event type: %w", err)
	}
	e.T, e.High = seconds*1000, kind == "H"
	return nil
}

// SpotTide is what the spot page's tide panel needs: the station it
// stands in for the beach with, and the events around the forecast.
type SpotTide struct {
	StationID   string      `json:"station"`
	StationName string      `json:"name"`
	Km          float64     `json:"km"`
	Subordinate bool        `json:"subordinate"`
	Events      []TideEvent `json:"events"`
	// Hourly predictions over the events' span, when the station has them.
	Hourly *TideHourly `json:"hourly,omitempty"`
}

// TideHourly is meters MLLW one hour apart from Start (epoch ms).
type TideHourly struct {
	Start int64     `json:"start"`
	V     []float64 `json:"v"`
}

type spotTideEntry struct {
	modTime int64
	set     *spotTideSet
}

var (
	spotTideMu    sync.Mutex
	spotTideCache = map[string]spotTideEntry{}
)

func loadSpotTides(staticDir string) (*spotTideSet, error) {
	path := filepath.Join(staticDir, spotTidesFile)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	spotTideMu.Lock()
	defer spotTideMu.Unlock()
	if cached, ok := spotTideCache[path]; ok && cached.modTime == info.ModTime().UnixNano() {
		return cached.set, nil
	}
	set, err := readSpotTides(path)
	if err != nil {
		return nil, err
	}
	spotTideCache[path] = spotTideEntry{modTime: info.ModTime().UnixNano(), set: set}
	return set, nil
}

func readSpotTides(path string) (*spotTideSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var set spotTideSet
	if err := json.NewDecoder(zr).Decode(&set); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	return &set, nil
}

// spotTide returns the spot's station and its events from one event
// before from to one event after to, so a curve drawn between events
// spans the whole window.
func spotTide(staticDir, spotID string, from, to time.Time) (SpotTide, bool) {
	set, err := loadSpotTides(staticDir)
	if err != nil {
		return SpotTide{}, false
	}
	match, ok := set.Spots[spotID]
	if !ok {
		return SpotTide{}, false
	}
	st, ok := set.Stations[match.Station]
	if !ok {
		return SpotTide{}, false
	}
	fromMs, toMs := from.UnixMilli(), to.UnixMilli()
	first, last := -1, -1
	for i, e := range st.HiLo {
		if e.T < fromMs {
			first = i // the latest event before the window
			continue
		}
		if first == -1 {
			first = i
		}
		last = i
		if e.T > toMs {
			break // the first event after the window
		}
	}
	if first == -1 || last <= first {
		return SpotTide{}, false // the file's events don't reach this window
	}
	events := st.HiLo[first : last+1]
	return SpotTide{
		StationID:   match.Station,
		StationName: st.Name,
		Km:          match.Km,
		Subordinate: st.Type == "S",
		Events:      events,
		Hourly:      hourlySpan(st, events[0].T, events[len(events)-1].T),
	}, true
}

// hourlySpan trims the station's hourly predictions to the whole hours
// covering [fromMs, toMs].
func hourlySpan(st *tideStation, fromMs, toMs int64) *TideHourly {
	if st.Hourly == nil || len(st.Hourly.V) == 0 {
		return nil
	}
	startMs := st.Hourly.Start * 1000
	const hourMs = int64(time.Hour / time.Millisecond)
	i := max(0, (fromMs-startMs)/hourMs)
	j := min(int64(len(st.Hourly.V))-1, (toMs-startMs+hourMs-1)/hourMs)
	if i >= j {
		return nil
	}
	return &TideHourly{Start: startMs + i*hourMs, V: st.Hourly.V[i : j+1]}
}
