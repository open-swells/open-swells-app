package main

// Per-spot forecasts from spot_forecasts.json.gz: the pipeline samples every
// spot at its nearest wet cell of the full-resolution (1/6 deg) wave grid,
// hourly to f120 and 3-hourly after, for the whole run in one file. This
// replaces reading the map's ~1.67 deg swell_partitions/wind grids, which
// remain the fallback for spots the file doesn't cover (a stale or missing
// file, or a spot added to the app but not yet to the pipeline's copy).
//
// Payload (see grib-parse-collect/spot_forecasts.py): "hours" is the run's
// forecast hour list and each spot holds one array per field aligned with
// it, null where a forecast hour failed.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const spotForecastsFile = "spot_forecasts.json.gz"

// floatSeries decodes a JSON number array with nulls as NaN. A dedicated
// decoder avoids allocating a *float64 per value: a run holds ~15M values.
type floatSeries []float32

func (s *floatSeries) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*s = nil
		return nil
	}
	if len(data) < 2 || data[0] != '[' || data[len(data)-1] != ']' {
		return errors.New("spot forecast series: expected array")
	}
	body := bytes.TrimSpace(data[1 : len(data)-1])
	if len(body) == 0 {
		*s = floatSeries{}
		return nil
	}
	out := make(floatSeries, 0, bytes.Count(body, []byte(","))+1)
	for len(body) > 0 {
		item := body
		if i := bytes.IndexByte(body, ','); i >= 0 {
			item, body = body[:i], body[i+1:]
		} else {
			body = nil
		}
		item = bytes.TrimSpace(item)
		if bytes.Equal(item, []byte("null")) {
			out = append(out, float32(math.NaN()))
			continue
		}
		v, err := strconv.ParseFloat(string(item), 32)
		if err != nil {
			return fmt.Errorf("spot forecast series: %w", err)
		}
		out = append(out, float32(v))
	}
	*s = out
	return nil
}

// at returns value i, or NaN when the series is short or absent.
func (s floatSeries) at(i int) float64 {
	if i < 0 || i >= len(s) {
		return math.NaN()
	}
	return float64(s[i])
}

type spotSeries struct {
	Cell      [2]float64  `json:"cell"` // [lat, lon] of the sampled grid cell
	Km        float64     `json:"km"`   // distance from the spot's sample point
	HS        floatSeries `json:"hs"`
	H1        floatSeries `json:"h1"`
	P1        floatSeries `json:"p1"`
	D1        floatSeries `json:"d1"`
	H2        floatSeries `json:"h2"`
	P2        floatSeries `json:"p2"`
	D2        floatSeries `json:"d2"`
	H3        floatSeries `json:"h3"`
	P3        floatSeries `json:"p3"`
	D3        floatSeries `json:"d3"`
	WindSpeed floatSeries `json:"ws"`
	WindDir   floatSeries `json:"wd"`
	// Locally generated wind sea, which the swell partitions exclude.
	// Absent from files written before the pipeline sampled it.
	HW floatSeries `json:"hw"`
	PW floatSeries `json:"pw"`
	DW floatSeries `json:"dw"`
}

type spotForecastSet struct {
	ForecastStart string                 `json:"forecast_start"`
	Hours         []int                  `json:"hours"`
	Spots         map[string]*spotSeries `json:"spots"`
}

type spotForecastEntry struct {
	modTime int64
	set     *spotForecastSet
}

var (
	spotForecastMu    sync.Mutex
	spotForecastCache = map[string]spotForecastEntry{}
)

// loadSpotForecasts returns the parsed file, cached by mtime like the grid
// loaders. A file from a different run than metadata.json (the pipeline
// skipped spots on a later run, leaving an old copy behind) is ignored so
// its values never land on the wrong timeline.
func loadSpotForecasts(staticDir string) (*spotForecastSet, error) {
	path := filepath.Join(staticDir, spotForecastsFile)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	spotForecastMu.Lock()
	defer spotForecastMu.Unlock()
	cached, ok := spotForecastCache[path]
	set := cached.set
	if !ok || cached.modTime != info.ModTime().UnixNano() {
		set, err = readSpotForecasts(path)
		if err != nil {
			return nil, err
		}
		spotForecastCache[path] = spotForecastEntry{modTime: info.ModTime().UnixNano(), set: set}
	}
	if start := windForecastStart(staticDir); set.ForecastStart != start {
		return nil, fmt.Errorf("%s is for run %s, metadata is %s", spotForecastsFile, set.ForecastStart, start)
	}
	return set, nil
}

func readSpotForecasts(path string) (*spotForecastSet, error) {
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
	var set spotForecastSet
	if err := json.NewDecoder(zr).Decode(&set); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	return &set, nil
}

// spotSeriesFor returns the spot's series from the current run's file.
func spotSeriesFor(staticDir, spotID string) (*spotForecastSet, *spotSeries, bool) {
	set, err := loadSpotForecasts(staticDir)
	if err != nil {
		return nil, nil, false
	}
	series, ok := set.Spots[spotID]
	return set, series, ok
}

// point converts hour index i into the grid-sample shape the forecast
// builder shares with the coarse-grid path; ok is false for a failed hour.
func (s *spotSeries) point(i int) (swellPoint, bool) {
	if math.IsNaN(s.HS.at(i)) {
		return swellPoint{}, false
	}
	p := swellPoint{Lat: s.Cell[0], Lon: s.Cell[1]}
	parts := [3][3]floatSeries{
		{s.H1, s.P1, s.D1},
		{s.H2, s.P2, s.D2},
		{s.H3, s.P3, s.D3},
	}
	for n, part := range parts {
		h, per, d := part[0].at(i), part[1].at(i), part[2].at(i)
		if !math.IsNaN(h) && !math.IsNaN(per) && !math.IsNaN(d) {
			p.H[n], p.P[n], p.D[n] = h, per, d
			p.Has[n] = true
		}
	}
	if h, per, d := s.HW.at(i), s.PW.at(i), s.DW.at(i); !math.IsNaN(h) && !math.IsNaN(per) && !math.IsNaN(d) {
		p.WindSea = waveTrain{H: h, P: per, D: d}
		p.HasWindSea = true
	}
	return p, true
}

// wind returns the spot's wind series, skipping hours without wind.
func (s *spotSeries) wind(hours []int) []WindSample {
	samples := []WindSample{}
	for i, hour := range hours {
		speed, dir := s.WindSpeed.at(i), s.WindDir.at(i)
		if math.IsNaN(speed) || math.IsNaN(dir) {
			continue
		}
		// Undo float32 storage noise (4.1 -> 4.0999999) so the /api/wind
		// payload carries the file's precision: 0.1 m/s, whole degrees.
		samples = append(samples, WindSample{Hour: hour, Speed: math.Round(speed*10) / 10, Dir: math.Round(dir)})
	}
	return samples
}
