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
//
// A run starts at its cycle time, so on its own a new run would empty out
// the hours of today that came before it. Each run's file is kept under
// spotForecastArchiveDir, and a loaded run is prefixed with up to
// spotForecastBackfillHours hours from the earlier runs (as negative
// forecast hours), so day views can show today midnight to midnight.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	spotForecastsFile = "spot_forecasts.json.gz"
	// Earlier runs' files, hard-linked here as spot_forecasts_<run>.json.gz.
	// publish.rules excludes the directory, so a publish never deletes it.
	spotForecastArchiveDir = "spot_forecast_runs"
	// A day either side of a run's start covers local midnight in every
	// time zone for as long as the run is current.
	spotForecastBackfillHours = 24
	forecastRunLayout         = "20060102_15Z"
)

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
		var opened os.FileInfo
		set, opened, err = readSpotForecasts(path)
		if err != nil {
			return nil, err
		}
		archiveSpotForecasts(staticDir, opened, set.ForecastStart)
		backfillSpotForecasts(staticDir, set)
		spotForecastCache[path] = spotForecastEntry{modTime: info.ModTime().UnixNano(), set: set}
	}
	if start := windForecastStart(staticDir); set.ForecastStart != start {
		return nil, fmt.Errorf("%s is for run %s, metadata is %s", spotForecastsFile, set.ForecastStart, start)
	}
	return set, nil
}

// readSpotForecasts also returns the opened file's info, which identifies
// the file that was read even if a publish has since replaced path.
func readSpotForecasts(path string) (*spotForecastSet, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, err
	}
	defer zr.Close()
	var set spotForecastSet
	if err := json.NewDecoder(zr).Decode(&set); err != nil {
		return nil, nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	return &set, info, nil
}

func archivedSpotForecastsPath(staticDir, run string) string {
	return filepath.Join(staticDir, spotForecastArchiveDir, "spot_forecasts_"+run+".json.gz")
}

// archiveSpotForecasts hard-links the current file into the archive under
// its run, so the run's hours outlive the publish that replaces it, and
// prunes runs too old to backfill anything. opened is the file that was
// read; a link that turns out to be a newer file (a publish landed in
// between) is discarded rather than archived under the wrong run.
func archiveSpotForecasts(staticDir string, opened os.FileInfo, run string) {
	start, err := time.Parse(forecastRunLayout, run)
	if err != nil {
		return
	}
	dst := archivedSpotForecastsPath(staticDir, run)
	if _, err := os.Stat(dst); err != nil {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			log.Printf("spot forecast archive: %v", err)
			return
		}
		tmp := dst + ".tmp"
		os.Remove(tmp)
		if err := os.Link(filepath.Join(staticDir, spotForecastsFile), tmp); err != nil {
			log.Printf("spot forecast archive: %v", err)
			return
		}
		if linked, err := os.Stat(tmp); err != nil || !os.SameFile(linked, opened) {
			os.Remove(tmp)
		} else if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			log.Printf("spot forecast archive: %v", err)
		}
	}

	cutoff := start.Add(-spotForecastBackfillHours * time.Hour)
	for archived, at := range archivedSpotForecastRuns(staticDir) {
		if at.Before(cutoff) {
			os.Remove(archivedSpotForecastsPath(staticDir, archived))
		}
	}
}

// archivedSpotForecastRuns maps each archived run to its start time.
func archivedSpotForecastRuns(staticDir string) map[string]time.Time {
	entries, _ := os.ReadDir(filepath.Join(staticDir, spotForecastArchiveDir))
	runs := map[string]time.Time{}
	for _, entry := range entries {
		run, ok := strings.CutPrefix(entry.Name(), "spot_forecasts_")
		if !ok {
			continue
		}
		if run, ok = strings.CutSuffix(run, ".json.gz"); !ok {
			continue
		}
		if at, err := time.Parse(forecastRunLayout, run); err == nil {
			runs[run] = at
		}
	}
	return runs
}

// fields lists the series aligned with the run's hours.
func (s *spotSeries) fields() []*floatSeries {
	return []*floatSeries{
		&s.HS, &s.H1, &s.P1, &s.D1, &s.H2, &s.P2, &s.D2, &s.H3, &s.P3, &s.D3,
		&s.WindSpeed, &s.WindDir, &s.HW, &s.PW, &s.DW,
	}
}

// backfillSpotForecasts prefixes set with the hours before its start, as
// negative forecast hours, taking each hour from the newest archived run
// that has it (the shortest lead time). Only the gap-free stretch ending
// at the run's start is added. Archives are read one at a time and only
// the needed hours kept, since a whole run is ~60 MB in memory.
func backfillSpotForecasts(staticDir string, set *spotForecastSet) {
	start, err := time.Parse(forecastRunLayout, set.ForecastStart)
	if err != nil || len(set.Hours) == 0 || set.Hours[0] != 0 {
		return
	}
	type archivedRun struct {
		run   string
		start time.Time
	}
	var earlier []archivedRun
	for run, at := range archivedSpotForecastRuns(staticDir) {
		if at.Before(start) {
			earlier = append(earlier, archivedRun{run, at})
		}
	}
	if len(earlier) == 0 {
		return
	}
	sort.Slice(earlier, func(i, j int) bool { return earlier[i].start.After(earlier[j].start) })

	// Index k holds forecast hour k-n.
	const n = spotForecastBackfillHours
	filled := make([]bool, n)
	prefix := make(map[string]*spotSeries, len(set.Spots))
	for id := range set.Spots {
		p := &spotSeries{}
		for _, field := range p.fields() {
			*field = make(floatSeries, n)
			for k := range *field {
				(*field)[k] = float32(math.NaN())
			}
		}
		prefix[id] = p
	}
	for _, archived := range earlier {
		old, _, err := readSpotForecasts(archivedSpotForecastsPath(staticDir, archived.run))
		if err != nil || old.ForecastStart != archived.run {
			continue
		}
		offset := int(archived.start.Sub(start).Hours())
		for i, hour := range old.Hours {
			k := offset + hour + n
			if k < 0 || k >= n || filled[k] {
				continue
			}
			filled[k] = true
			for id, dst := range prefix {
				src, ok := old.Spots[id]
				if !ok {
					continue
				}
				srcFields := src.fields()
				for f, field := range dst.fields() {
					(*field)[k] = float32(srcFields[f].at(i))
				}
			}
		}
	}

	first := n
	for first > 0 && filled[first-1] {
		first--
	}
	if first == n {
		return
	}
	hours := make([]int, 0, n-first+len(set.Hours))
	for k := first; k < n; k++ {
		hours = append(hours, k-n)
	}
	set.Hours = append(hours, set.Hours...)
	for id, series := range set.Spots {
		pre := prefix[id].fields()
		for f, field := range series.fields() {
			*field = append(append(floatSeries{}, (*pre[f])[first:]...), *field...)
		}
	}
}

// spotSeriesFor returns the spot's series from the current run's file,
// including any hours backfilled from earlier runs.
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
