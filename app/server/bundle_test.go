package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestBundle writes a grid bundle the way pipeline/bundle.py does.
// values[hour][var] is a row-major lattice; NaN becomes NODATA.
func writeTestBundle(t *testing.T, path string, lat, lon []float64, hours []int, vars []bundleVar, values map[int]map[string][]float64) {
	t.Helper()
	const nodata = -32768
	var body bytes.Buffer
	var chunks []int64
	for _, hour := range hours {
		for _, v := range vars {
			raw := make([]byte, 2*len(lat)*len(lon))
			for i, x := range values[hour][v.Name] {
				q := int16(nodata)
				if !math.IsNaN(x) {
					q = int16(math.Round(x / v.Scale))
				}
				binary.LittleEndian.PutUint16(raw[2*i:], uint16(q))
			}
			var gz bytes.Buffer
			zw := gzip.NewWriter(&gz)
			zw.Name = chunkTag("", hour, v.Name)
			zw.Write(raw)
			zw.Close()
			chunks = append(chunks, int64(gz.Len()))
			body.Write(gz.Bytes())
		}
	}
	header, err := json.Marshal(map[string]any{
		"format": "openswells-bundle", "version": 1, "kind": "grid",
		"hours": hours, "vars": vars, "chunks": chunks,
		"lat": lat, "lon": lon, "nodata": nodata,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.WriteString("OSB1")
	binary.Write(&out, binary.LittleEndian, uint32(len(header)))
	out.Write(header)
	out.Write(body.Bytes())
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeMetadata writes a run's metadata.json, bumping its mtime past any
// earlier one so the change is seen within the same second.
func writeMetadata(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "metadata.json")
	stamp := time.Now()
	if info, err := os.Stat(path); err == nil {
		stamp = info.ModTime().Add(time.Second)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, stamp, stamp)
}

func axis(start, step float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Round((start+float64(i)*step)*100) / 100
	}
	return out
}

// randomField fills a lattice with values rounded to scale, NaN where land.
func randomField(r *rand.Rand, land []bool, scale, max float64) []float64 {
	out := make([]float64, len(land))
	for i := range out {
		if land[i] {
			out[i] = math.NaN()
			continue
		}
		out[i] = math.Round(r.Float64()*max/scale) * scale
	}
	return out
}

// writeLegacyPoints writes the same lattice as old-format GeoJSON points:
// one feature per cell where keep is true, row-major like the pipeline.
func writeLegacyPoints(t *testing.T, path string, lat, lon []float64, keep func(cell int) bool, props func(cell int) string) {
	t.Helper()
	var features []string
	for i, la := range lat {
		for j, lo := range lon {
			cell := i*len(lon) + j
			if !keep(cell) {
				continue
			}
			features = append(features, fmt.Sprintf(`{"type":"Feature","geometry":{"type":"Point","coordinates":[%g,%g]},"properties":{%s}}`, lo, la, props(cell)))
		}
	}
	doc := `{"type":"FeatureCollection","features":[` + strings.Join(features, ",") + `]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSwellBundleMatchesLegacyNearest(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	lat := axis(85, -5.0/3, 40) // north -> south, like the composite
	lon := axis(0, 5.0/3, 216)  // 0..360
	cells := len(lat) * len(lon)
	land := make([]bool, cells)
	for i := range land {
		land[i] = r.Float64() < 0.4
	}
	vars := []bundleVar{}
	for k := 1; k <= 3; k++ {
		vars = append(vars, bundleVar{fmt.Sprintf("h%d", k), 0.01}, bundleVar{fmt.Sprintf("p%d", k), 0.1}, bundleVar{fmt.Sprintf("d%d", k), 1})
	}
	fields := map[string][]float64{}
	for k := 1; k <= 3; k++ {
		partLand := append([]bool(nil), land...)
		if k > 1 { // secondary systems are absent in more places
			for i := range partLand {
				partLand[i] = partLand[i] || r.Float64() < 0.3
			}
		}
		fields[fmt.Sprintf("h%d", k)] = randomField(r, partLand, 0.01, 6)
		fields[fmt.Sprintf("p%d", k)] = randomField(r, partLand, 0.1, 20)
		fields[fmt.Sprintf("d%d", k)] = randomField(r, partLand, 1, 359)
	}

	legacyDir, bundleDir := t.TempDir(), t.TempDir()
	writeLegacyPoints(t, filepath.Join(legacyDir, "swell_partitions_000.geojson"), lat, lon,
		func(c int) bool {
			return !math.IsNaN(fields["h1"][c]) || !math.IsNaN(fields["h2"][c]) || !math.IsNaN(fields["h3"][c])
		},
		func(c int) string {
			var props []string
			for k := 1; k <= 3; k++ {
				if h := fields[fmt.Sprintf("h%d", k)][c]; !math.IsNaN(h) {
					props = append(props, fmt.Sprintf(`"h%d":%g,"p%d":%g,"d%d":%g`, k, h, k, fields[fmt.Sprintf("p%d", k)][c], k, fields[fmt.Sprintf("d%d", k)][c]))
				}
			}
			return strings.Join(props, ",")
		})
	writeTestBundle(t, filepath.Join(bundleDir, "swell_r.bin"), lat, lon, []int{0}, vars, map[int]map[string][]float64{0: fields})
	writeMetadata(t, bundleDir, `{"bundles":{"swell":"swell_r.bin"}}`)

	for i := 0; i < 2000; i++ {
		qLat := r.Float64()*170 - 85
		qLon := r.Float64()*360 - 180
		want, wantOK, err := swellNearest(legacyDir, 0, qLat, qLon)
		if err != nil {
			t.Fatal(err)
		}
		got, gotOK, err := swellNearest(bundleDir, 0, qLat, qLon)
		if err != nil {
			t.Fatal(err)
		}
		if gotOK != wantOK || (wantOK && !swellPointsClose(got, want)) {
			t.Fatalf("(%.3f, %.3f): bundle %+v %v, legacy %+v %v", qLat, qLon, got, gotOK, want, wantOK)
		}
	}
	if _, _, err := swellNearest(bundleDir, 3, 0, 0); err == nil {
		t.Fatal("hour missing from the bundle should be an error")
	}
}

func swellPointsClose(a, b swellPoint) bool {
	if math.Abs(a.Lat-b.Lat) > 1e-9 || math.Abs(a.Lon-b.Lon) > 1e-9 || a.Has != b.Has {
		return false
	}
	for i := 0; i < 3; i++ {
		if math.Abs(a.H[i]-b.H[i]) > 1e-9 || math.Abs(a.P[i]-b.P[i]) > 1e-9 || math.Abs(a.D[i]-b.D[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func TestWindAndNwpsBundlesMatchLegacyNearest(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	lat := axis(31.5, 0.03, 60) // NWPS-like: ascending, fine
	lon := axis(239.0, 0.03, 80)
	cells := len(lat) * len(lon)
	land := make([]bool, cells)
	for i := range land {
		land[i] = r.Float64() < 0.5
	}
	wind := map[string][]float64{
		"s": randomField(r, land, 0.1, 20), "d": randomField(r, land, 1, 359),
		"u": randomField(r, land, 0.1, 10), "v": randomField(r, land, 0.1, 10),
	}
	nwps := map[string][]float64{
		"h": randomField(r, land, 0.01, 4), "s": randomField(r, land, 0.01, 3),
		"p": randomField(r, land, 0.1, 18), "d": randomField(r, land, 1, 359),
	}
	wet := func(c int) bool { return !land[c] }

	legacyDir, bundleDir := t.TempDir(), t.TempDir()
	writeLegacyPoints(t, filepath.Join(legacyDir, "wind_006.geojson"), lat, lon, wet, func(c int) string {
		return fmt.Sprintf(`"s":%g,"d":%g,"u":%g,"v":%g`, wind["s"][c], wind["d"][c], wind["u"][c], wind["v"][c])
	})
	writeLegacyPoints(t, filepath.Join(legacyDir, "nwps_points_lox_cg1_006.geojson"), lat, lon, wet, func(c int) string {
		return fmt.Sprintf(`"h":%g,"s":%g,"p":%g,"d":%g`, nwps["h"][c], nwps["s"][c], nwps["p"][c], nwps["d"][c])
	})
	writeTestBundle(t, filepath.Join(bundleDir, "wind_r.bin"), lat, lon, []int{6},
		[]bundleVar{{"s", 0.1}, {"d", 1}, {"u", 0.1}, {"v", 0.1}}, map[int]map[string][]float64{6: wind})
	writeTestBundle(t, filepath.Join(bundleDir, "nwps_points_lox_cg1_r.bin"), lat, lon, []int{6},
		[]bundleVar{{"h", 0.01}, {"s", 0.01}, {"p", 0.1}, {"d", 1}}, map[int]map[string][]float64{6: nwps})
	writeMetadata(t, bundleDir, `{"bundles":{"wind":"wind_r.bin"}}`)

	legacyDomain := nwpsDomain{WFO: "lox", Grid: "cg1", Hours: []int{6}}
	domain := legacyDomain
	domain.File = "nwps_points_lox_cg1_r.bin"
	for i := 0; i < 2000; i++ {
		qLat := 31.4 + r.Float64()*2
		qLon := -121.1 + r.Float64()*2.6
		wantW, wantOK, _ := windNearest(legacyDir, 6, qLat, qLon)
		gotW, gotOK, err := windNearest(bundleDir, 6, qLat, qLon)
		if err != nil {
			t.Fatal(err)
		}
		if gotOK != wantOK || (wantOK && (math.Abs(gotW.S-wantW.S) > 1e-9 || gotW.D != wantW.D || gotW.Lat != wantW.Lat)) {
			t.Fatalf("wind (%.3f, %.3f): bundle %+v %v, legacy %+v %v", qLat, qLon, gotW, gotOK, wantW, wantOK)
		}
		wantN, wantDist, wantNOK := nwpsNearest(legacyDir, legacyDomain, 6, qLat, qLon)
		gotN, gotDist, gotNOK := nwpsNearest(bundleDir, domain, 6, qLat, qLon)
		if gotNOK != wantNOK || (wantNOK && (math.Abs(gotN.H-wantN.H) > 1e-9 || math.Abs(gotN.S-wantN.S) > 1e-9 ||
			math.Abs(gotN.P-wantN.P) > 1e-9 || gotN.D != wantN.D || math.Abs(gotDist-wantDist) > 1e-9)) {
			t.Fatalf("nwps (%.3f, %.3f): bundle %+v %v, legacy %+v %v", qLat, qLon, gotN, gotNOK, wantN, wantNOK)
		}
	}
}

func TestRunBundlesFollowMetadata(t *testing.T) {
	dir := t.TempDir()
	lat, lon := []float64{34}, []float64{242}
	vars := []bundleVar{{"s", 0.1}, {"d", 1}, {"u", 0.1}, {"v", 0.1}}
	publish := func(run string, speed float64, hours []int) {
		values := map[int]map[string][]float64{}
		for _, h := range hours {
			values[h] = map[string][]float64{"s": {speed}, "d": {270}, "u": {1}, "v": {1}}
		}
		writeTestBundle(t, filepath.Join(dir, "wind_"+run+".bin"), lat, lon, hours, vars, values)
		writeMetadata(t, dir, `{"bundles":{"wind":"wind_`+run+`.bin"}}`)
	}
	publish("20261028_06Z", 4, []int{0})
	if p, ok, err := windNearest(dir, 0, 34, -118); err != nil || !ok || p.S != 4 {
		t.Fatalf("first run: %+v %v %v", p, ok, err)
	}
	// The next run is a new file; the previous one stays on disk.
	publish("20261028_12Z", 7, []int{0, 3})
	if p, ok, err := windNearest(dir, 0, 34, -118); err != nil || !ok || p.S != 7 {
		t.Fatalf("after the next run: %+v %v %v", p, ok, err)
	}
	if _, _, err := windNearest(dir, 3, 34, -118); err != nil {
		t.Fatalf("new hour: %v", err)
	}
	// Only the current run's bundle stays in memory.
	gridBundleMu.Lock()
	_, stale := gridBundleCache[filepath.Join(dir, "wind_20261028_06Z.bin")]
	gridBundleMu.Unlock()
	if stale {
		t.Error("the previous run's bundle is still cached")
	}
}

func TestGridBundleRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	for name, contents := range map[string][]byte{
		"magic.bin":     []byte("NOPE\x02\x00\x00\x00{}"),
		"truncated.bin": append([]byte("OSB1\xff\x00\x00\x00"), []byte("{}")...),
		"chunks.bin":    append([]byte("OSB1\x2a\x00\x00\x00"), []byte(`{"kind":"grid","hours":[0],"vars":[],"chunks":[1]}`)[:42]...),
	} {
		path := filepath.Join(dir, name)
		os.WriteFile(path, contents, 0o644)
		if _, err := loadGridBundle(path); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestGridBundleRefusesChunksUnderTheWrongName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wind_r.bin")
	writeMetadata(t, dir, `{"bundles":{"wind":"wind_r.bin"}}`)
	vars := []bundleVar{{"s", 0.1}, {"d", 1}, {"u", 0.1}, {"v", 0.1}}
	values := map[string][]float64{"s": {4}, "d": {270}, "u": {1}, "v": {1}}
	writeTestBundle(t, path, []float64{34}, []float64{242}, []int{3}, vars, map[int]map[string][]float64{3: values})

	// Relabel the hour in the header: its offsets now claim hour 6.
	raw, _ := os.ReadFile(path)
	n := binary.LittleEndian.Uint32(raw[4:8])
	header := bytes.Replace(raw[8:8+n], []byte(`"hours":[3]`), []byte(`"hours":[6]`), 1)
	if len(header) != int(n) {
		t.Fatal("header edit changed its length")
	}
	copy(raw[8:], header)
	os.WriteFile(path, raw, 0o644)

	if _, _, err := windNearest(dir, 6, 34, -118); err == nil || !strings.Contains(err.Error(), `want "/6/s"`) {
		t.Fatalf("expected a chunk name error, got %v", err)
	}
}
