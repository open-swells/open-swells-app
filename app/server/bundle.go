package main

// Forecast bundles: the pipeline writes one file per map product per run
// (swell_<run>.bin, wind_<run>.bin, nwps_points_<wfo>_<grid>_<run>.bin,
// listed in metadata.json; layout documented in pipeline/bundle.py) instead
// of one GeoJSON file per forecast hour. A grid
// bundle is a JSON header with the lattice axes, forecast hours and vars,
// followed by one gzipped row-major int16 array per (hour, var).
//
// Only the header is read when a bundle is opened. An hour's arrays are
// decoded on first use and kept until the file changes, as 2 bytes per cell
// per var: a lookup is index math over the lattice instead of a scan of
// decoded GeoJSON points.

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

var bundleMagic = []byte("OSB1")

type bundleVar struct {
	Name  string  `json:"name"`
	Scale float64 `json:"scale"`
}

type bundleHeader struct {
	Kind   string      `json:"kind"`
	Run    string      `json:"run"`
	Hours  []int       `json:"hours"`
	Vars   []bundleVar `json:"vars"`
	Chunks []int64     `json:"chunks"`
	Lat    []float64   `json:"lat"`
	Lon    []float64   `json:"lon"`
	Nodata int16       `json:"nodata"`
}

type gridBundle struct {
	path    string
	modTime int64
	header  bundleHeader
	offsets []int64 // file offset of each chunk, header.Chunks order
	hourAt  map[int]int
	varAt   map[string]int
	cells   int
	// Per var: raw / divisor is the value. Dividing by 100 lands on the
	// nearest float64 to the decimal the pipeline rounded to (0.57), where
	// multiplying by 0.01 doesn't (0.5700000000000001).
	divisors []float64

	mu     sync.Mutex
	frames map[int]*gridFrame
}

// gridFrame is one forecast hour: every var's int16 lattice, var-major.
type gridFrame struct {
	bundle *gridBundle
	raw    []int16
}

var (
	gridBundleMu    sync.Mutex
	gridBundleCache = map[string]*gridBundle{}
)

// runBundles is what the readers need from metadata.json: this run's bundle
// file per product, and the NWPS domains with theirs.
type runBundles struct {
	Bundles    map[string]string `json:"bundles"`
	NwpsPoints []nwpsDomain      `json:"nwps_points"`
}

var (
	runBundlesMu      sync.Mutex
	runBundlesDir     string
	runBundlesModTime int64
	runBundlesValue   runBundles
)

// currentRunBundles reads metadata.json's bundle names, re-parsing only when
// the file changes. A new run's names replace the old run's, and bundles
// the new metadata doesn't list are dropped from the cache: the previous
// run's files stay on disk for maps opened before it, but the server reads
// only the current run.
func currentRunBundles(staticDir string) runBundles {
	path := filepath.Join(staticDir, "metadata.json")
	info, err := os.Stat(path)
	if err != nil {
		return runBundles{}
	}
	runBundlesMu.Lock()
	defer runBundlesMu.Unlock()
	if runBundlesDir == staticDir && runBundlesModTime == info.ModTime().UnixNano() {
		return runBundlesValue
	}
	var value runBundles
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, &value)
	}
	runBundlesDir, runBundlesModTime, runBundlesValue = staticDir, info.ModTime().UnixNano(), value

	listed := map[string]bool{}
	for _, name := range value.Bundles {
		listed[filepath.Join(staticDir, name)] = true
	}
	for _, d := range value.NwpsPoints {
		listed[filepath.Join(staticDir, d.File)] = true
	}
	gridBundleMu.Lock()
	for cached := range gridBundleCache {
		if filepath.Dir(cached) == filepath.Clean(staticDir) && !listed[cached] {
			delete(gridBundleCache, cached)
		}
	}
	gridBundleMu.Unlock()
	return value
}

// runBundlePath is this run's bundle for a product ("swell", "wind"), or ""
// for a run from before the bundles.
func runBundlePath(staticDir, product string) string {
	name := currentRunBundles(staticDir).Bundles[product]
	if name == "" {
		return ""
	}
	return filepath.Join(staticDir, name)
}

// loadGridBundle returns the bundle at path, re-reading the header only
// when the file's mtime changes (a new run replaces the file).
func loadGridBundle(path string) (*gridBundle, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	gridBundleMu.Lock()
	defer gridBundleMu.Unlock()
	if cached, ok := gridBundleCache[path]; ok && cached.modTime == info.ModTime().UnixNano() {
		return cached, nil
	}
	b, err := readGridBundleHeader(path)
	if err != nil {
		return nil, err
	}
	gridBundleCache[path] = b
	return b, nil
}

func readGridBundleHeader(path string) (*gridBundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var prefix [8]byte
	if _, err := io.ReadFull(f, prefix[:]); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !bytes.Equal(prefix[:4], bundleMagic) {
		return nil, fmt.Errorf("%s is not a forecast bundle", path)
	}
	headerLen := int64(binary.LittleEndian.Uint32(prefix[4:]))
	if headerLen > info.Size()-8 {
		return nil, fmt.Errorf("%s: truncated header", path)
	}
	rawHeader := make([]byte, headerLen)
	if _, err := io.ReadFull(f, rawHeader); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	b := &gridBundle{
		path:    path,
		modTime: info.ModTime().UnixNano(),
		hourAt:  map[int]int{},
		varAt:   map[string]int{},
		frames:  map[int]*gridFrame{},
	}
	if err := json.Unmarshal(rawHeader, &b.header); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	h := &b.header
	if h.Kind != "grid" {
		return nil, fmt.Errorf("%s: %q bundle, want grid", path, h.Kind)
	}
	if len(h.Chunks) != len(h.Hours)*len(h.Vars) {
		return nil, fmt.Errorf("%s: %d chunks for %d hours x %d vars", path, len(h.Chunks), len(h.Hours), len(h.Vars))
	}
	offset := 8 + headerLen
	b.offsets = make([]int64, len(h.Chunks))
	for i, n := range h.Chunks {
		b.offsets[i] = offset
		offset += n
	}
	if offset > info.Size() {
		return nil, fmt.Errorf("%s: truncated data", path)
	}
	for i, hour := range h.Hours {
		b.hourAt[hour] = i
	}
	for i, v := range h.Vars {
		b.varAt[v.Name] = i
		if v.Scale <= 0 {
			return nil, fmt.Errorf("%s: var %s has scale %v", path, v.Name, v.Scale)
		}
		divisor := 1 / v.Scale
		if v.Scale < 1 {
			divisor = math.Round(divisor)
		}
		b.divisors = append(b.divisors, divisor)
	}
	b.cells = len(h.Lat) * len(h.Lon)
	return b, nil
}

func chunkTag(run string, hour int, name string) string {
	return fmt.Sprintf("%s/%d/%s", run, hour, name)
}

func (b *gridBundle) hasHour(hour int) bool {
	_, ok := b.hourAt[hour]
	return ok
}

// frame decodes (once) every var of one forecast hour.
func (b *gridBundle) frame(hour int) (*gridFrame, error) {
	index, ok := b.hourAt[hour]
	if !ok {
		return nil, fmt.Errorf("%s: no hour %d", b.path, hour)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if f, ok := b.frames[hour]; ok {
		return f, nil
	}

	file, err := os.Open(b.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || info.ModTime().UnixNano() != b.modTime {
		// Replaced since the header was read; the next load re-reads it.
		return nil, fmt.Errorf("%s changed while reading", b.path)
	}
	nvars := len(b.header.Vars)
	first := index * nvars
	start := b.offsets[first]
	var length int64
	for _, n := range b.header.Chunks[first : first+nvars] {
		length += n
	}
	compressed := make([]byte, length)
	if _, err := file.ReadAt(compressed, start); err != nil {
		return nil, fmt.Errorf("%s: %w", b.path, err)
	}

	raw := make([]int16, nvars*b.cells)
	buf := make([]byte, 2*b.cells)
	zr := new(gzip.Reader)
	pos := int64(0)
	for v := 0; v < nvars; v++ {
		n := b.header.Chunks[first+v]
		if err := zr.Reset(bytes.NewReader(compressed[pos : pos+n])); err != nil {
			return nil, fmt.Errorf("%s hour %d: %w", b.path, hour, err)
		}
		// Each chunk names itself (pipeline/bundle.py chunk_tag): a header
		// whose offsets no longer match the file can't pass off another
		// hour's data.
		if want := chunkTag(b.header.Run, hour, b.header.Vars[v].Name); zr.Header.Name != want {
			return nil, fmt.Errorf("%s: chunk %q, want %q", b.path, zr.Header.Name, want)
		}
		if _, err := io.ReadFull(zr, buf); err != nil {
			return nil, fmt.Errorf("%s hour %d: %w", b.path, hour, err)
		}
		if _, err := zr.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s hour %d: chunk is not %d cells", b.path, hour, b.cells)
		}
		dst := raw[v*b.cells : (v+1)*b.cells]
		for i := range dst {
			dst[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
		}
		pos += n
	}
	f := &gridFrame{bundle: b, raw: raw}
	b.frames[hour] = f
	return f, nil
}

// value is var v at a cell (row-major index), false where it is NODATA.
func (f *gridFrame) value(v, cell int) (float64, bool) {
	if v < 0 {
		return 0, false
	}
	raw := f.raw[v*f.bundle.cells+cell]
	if raw == f.bundle.header.Nodata {
		return 0, false
	}
	return float64(raw) / f.bundle.divisors[v], true
}

// gridCell is a lattice cell near a query point; Dist is in squared
// degrees with longitude wrapped, the metric the GeoJSON scans used.
type gridCell struct {
	Index    int
	Lat, Lon float64
	Dist     float64
}

// nearbyCells lists the cells within sqrt(maxDist2) degrees of (lat, lon),
// nearest first. Ties keep row-major (file) order, so the first valid cell
// in the list is the one the old nearest-point scans picked. The grid may
// use 0..360 longitudes while callers use -180..180.
func (b *gridBundle) nearbyCells(lat, lon, maxDist2 float64) []gridCell {
	radius := math.Sqrt(maxDist2)
	var cols []int
	var dlngs []float64
	for j, cellLon := range b.header.Lon {
		dlng := math.Abs(math.Mod(cellLon-lon, 360))
		if dlng > 180 {
			dlng = 360 - dlng
		}
		if dlng <= radius {
			cols = append(cols, j)
			dlngs = append(dlngs, dlng)
		}
	}
	var cells []gridCell
	nlon := len(b.header.Lon)
	for i, cellLat := range b.header.Lat {
		dlat := cellLat - lat
		if math.Abs(dlat) > radius {
			continue
		}
		for k, j := range cols {
			dist := dlngs[k]*dlngs[k] + dlat*dlat
			if dist <= maxDist2 {
				cells = append(cells, gridCell{Index: i*nlon + j, Lat: cellLat, Lon: b.header.Lon[j], Dist: dist})
			}
		}
	}
	sort.Slice(cells, func(a, c int) bool {
		if cells[a].Dist != cells[c].Dist {
			return cells[a].Dist < cells[c].Dist
		}
		return cells[a].Index < cells[c].Index
	})
	return cells
}

func (b *gridBundle) varIndex(name string) int {
	if i, ok := b.varAt[name]; ok {
		return i
	}
	return -1
}
