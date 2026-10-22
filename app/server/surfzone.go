package main

// Beach forecasts: NWS Surf Zone Forecast (SRF) products, fetched from the
// tgftp text server and parsed into structured per-day periods.
//
//   - One text file per NWS public coastal zone at
//     tgftp.nws.noaa.gov/data/forecasts/marine/surf_zone/{state}/{zone}.txt,
//     reissued once or twice a day by the local forecast office with a
//     1–2 day horizon.
//   - The skeleton is common to every office — ".PERIOD..." day sections
//     of "Field.....Value." lines — but the field set varies (west coast
//     offices add swell remarks, Florida adds heat index and waterspouts),
//     so unrecognized fields are kept verbatim in Fields rather than
//     dropped, and the raw text is preserved as a rendering fallback.
//   - A file may cover several zones at once (e.g. "CAZ349-350-"); it is
//     stored once per zone with that zone's own name.
//
// Zones live in memory only: the source files are tiny and always
// refetchable, so unlike the station list nothing is persisted.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	surfZoneBaseURL      = "https://tgftp.nws.noaa.gov/data/forecasts/marine/surf_zone/"
	surfZoneGeoURL       = "https://api.weather.gov/zones/forecast/"
	surfZoneRefreshEvery = time.Hour
	// api.weather.gov requires an identifying User-Agent
	surfZoneUserAgent = "openswells (github.com/open-swells/open-swells-app)"
)

var surfZoneClient = &http.Client{Timeout: 30 * time.Second}

var errZoneNotFound = errors.New("zone not in api.weather.gov")

type SurfZonePeriod struct {
	Name       string            `json:"name"`                 // "TODAY", "THURSDAY", ...
	RipRisk    string            `json:"ripRisk,omitempty"`    // Low / Moderate / High
	SurfHeight string            `json:"surfHeight,omitempty"` // office wording, e.g. "2 to 4 feet"
	SurfMinFt  *float64          `json:"surfMinFt,omitempty"`
	SurfMaxFt  *float64          `json:"surfMaxFt,omitempty"`
	WaterTemp  string            `json:"waterTemp,omitempty"`
	Weather    string            `json:"weather,omitempty"`
	Winds      string            `json:"winds,omitempty"`
	Remarks    string            `json:"remarks,omitempty"` // west coast offices name the swells here
	Tides      []string          `json:"tides,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"` // office-specific extras, verbatim
}

type SurfZoneForecast struct {
	ZoneID    string           `json:"zoneId"` // e.g. CAZ043
	ZoneName  string           `json:"zoneName"`
	Beaches   string           `json:"beaches,omitempty"` // "Including the beaches of ..." line
	State     string           `json:"state"`             // tgftp directory, e.g. "ca"
	Office    string           `json:"office"`            // issuing WFO, e.g. KSGX
	Issued    time.Time        `json:"issued"`
	Expires   time.Time        `json:"expires"`
	Headlines []string         `json:"headlines,omitempty"` // "...HIGH RIP CURRENT RISK..."
	Periods   []SurfZonePeriod `json:"periods"`
	Raw       string           `json:"raw,omitempty"` // full product text, detail responses only
	// zone boundary GeoJSON, attached by handleZone for the map outline
	Geometry json.RawMessage `json:"geometry,omitempty"`
}

// surfZoneGeo is a zone's marker anchor and boundary, from api.weather.gov.
type surfZoneGeo struct {
	Lat, Lon float64
	Geometry json.RawMessage // GeoJSON geometry (Polygon or MultiPolygon)
}

type SurfZoneStore struct {
	states []string
	db     *sql.DB

	mu       sync.RWMutex
	byZone   map[string]SurfZoneForecast
	geo      map[string]surfZoneGeo   // zone id -> geometry, db-backed
	geoMiss  map[string]bool          // zones api.weather.gov doesn't know (404)
	lastMod  map[string]string        // file URL -> Last-Modified, for conditional GETs
	rings    map[string][][][]float64 // zone id -> parsed boundary rings, for point lookup
	fetchErr error                    // last refresh error, surfaced via /healthz
}

func NewSurfZoneStore(db *sql.DB, states []string) *SurfZoneStore {
	clean := make([]string, 0, len(states))
	for _, st := range states {
		if st = strings.ToLower(strings.TrimSpace(st)); st != "" {
			clean = append(clean, st)
		}
	}
	return &SurfZoneStore{
		states:  clean,
		db:      db,
		byZone:  map[string]SurfZoneForecast{},
		geo:     map[string]surfZoneGeo{},
		geoMiss: map[string]bool{},
		lastMod: map[string]string{},
		rings:   map[string][][][]float64{},
	}
}

// LastError reports the most recent refresh failure (nil when healthy).
func (s *SurfZoneStore) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fetchErr
}

func (s *SurfZoneStore) ZoneCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byZone)
}

// Get returns the cached forecast for a zone id (already uppercased ids
// only; use surfZoneIDRe to validate user input first).
func (s *SurfZoneStore) Get(zoneID string) (SurfZoneForecast, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.byZone[zoneID]
	return f, ok
}

// LoadGeometry warms the geometry cache from the surf_zones table so
// restarts don't refetch boundaries from api.weather.gov.
func (s *SurfZoneStore) LoadGeometry() error {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT zone_id, latitude, longitude, geometry FROM surf_zones`)
	if err != nil {
		return err
	}
	defer rows.Close()
	loaded := map[string]surfZoneGeo{}
	for rows.Next() {
		var id, geom string
		var g surfZoneGeo
		if err := rows.Scan(&id, &g.Lat, &g.Lon, &geom); err != nil {
			return err
		}
		g.Geometry = json.RawMessage(geom)
		loaded[id] = g
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.geo = loaded
	s.mu.Unlock()
	return nil
}

// RunRefresher refreshes once at startup and then hourly. Run as a goroutine.
func (s *SurfZoneStore) RunRefresher() {
	for {
		if err := s.refresh(); err != nil {
			log.Printf("surf zone refresh: %v", err)
		}
		time.Sleep(surfZoneRefreshEvery)
	}
}

var surfZoneFileRe = regexp.MustCompile(`href="([a-z]{2}z\d{3}\.txt)"`)

// refresh walks each configured state directory and re-parses files that
// changed since the last pass. A failure on one file or state never blocks
// the others, and stale entries are kept over nothing.
func (s *SurfZoneStore) refresh() error {
	var firstErr error
	zones := 0
	for _, state := range s.states {
		listing, _, err := s.fetchURL(surfZoneBaseURL+state+"/", "")
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s listing: %w", state, err)
			}
			continue
		}
		names := surfZoneFileRe.FindAllStringSubmatch(listing, -1)
		for _, m := range names {
			url := surfZoneBaseURL + state + "/" + m[1]
			s.mu.RLock()
			prevMod := s.lastMod[url]
			s.mu.RUnlock()

			body, mod, err := s.fetchURL(url, prevMod)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", m[1], err)
				}
				continue
			}
			if body == "" { // 304: unchanged since last parse
				continue
			}
			now := time.Now().UTC()
			forecasts, err := parseSurfZoneProduct(body, state, now)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("parse %s: %w", m[1], err)
				}
				continue
			}
			s.mu.Lock()
			s.lastMod[url] = mod
			for _, f := range forecasts {
				// Dead files linger in the directory for years (old zone
				// numberings, discontinued products) and can shadow a live
				// zone: only expired products are safe to drop, and a newer
				// issuance is never replaced by an older one.
				if !f.Expires.IsZero() && f.Expires.Before(now) {
					continue
				}
				if prev, ok := s.byZone[f.ZoneID]; ok && prev.Issued.After(f.Issued) {
					continue
				}
				s.byZone[f.ZoneID] = f
				zones++
			}
			s.mu.Unlock()
		}
	}
	if err := s.fetchMissingGeometry(); err != nil && firstErr == nil {
		firstErr = err
	}

	s.mu.Lock()
	s.fetchErr = firstErr
	total := len(s.byZone)
	s.mu.Unlock()
	if zones > 0 {
		log.Printf("surf zone refresh: %d zones updated (%d cached)", zones, total)
	}
	return firstErr
}

// fetchMissingGeometry pulls boundaries from api.weather.gov for any cached
// zone the surf_zones table doesn't know yet. Boundaries are effectively
// static, so each zone is fetched once and persisted.
func (s *SurfZoneStore) fetchMissingGeometry() error {
	s.mu.RLock()
	var missing []string
	for id := range s.byZone {
		if _, ok := s.geo[id]; !ok && !s.geoMiss[id] {
			missing = append(missing, id)
		}
	}
	s.mu.RUnlock()
	sort.Strings(missing)

	var firstErr error
	for _, id := range missing {
		g, err := fetchZoneGeometry(id)
		if err != nil {
			// Some zones simply aren't in the NWS zone API (e.g. CAZ364,
			// a newer LOX zone). They get no marker; remember that instead
			// of erroring on every refresh, and retry only after a restart.
			if errors.Is(err, errZoneNotFound) {
				log.Printf("surf zone geometry: %s not in api.weather.gov, skipping", id)
				s.mu.Lock()
				s.geoMiss[id] = true
				s.mu.Unlock()
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("geometry %s: %w", id, err)
			}
			continue
		}
		if s.db != nil {
			if _, err := s.db.Exec(
				`INSERT INTO surf_zones (zone_id, latitude, longitude, geometry, updated_at)
				 VALUES (?, ?, ?, ?, ?)
				 ON CONFLICT(zone_id) DO UPDATE SET
				     latitude = excluded.latitude,
				     longitude = excluded.longitude,
				     geometry = excluded.geometry,
				     updated_at = excluded.updated_at`,
				id, g.Lat, g.Lon, string(g.Geometry), time.Now().UTC().Format(time.RFC3339),
			); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("persist geometry %s: %w", id, err)
			}
		}
		s.mu.Lock()
		s.geo[id] = g
		s.mu.Unlock()
	}
	if len(missing) > 0 {
		log.Printf("surf zone geometry: fetched %d missing zones", len(missing))
	}
	return firstErr
}

// --- Zone lookup for surf spots -------------------------------------------

// A spot outside every zone polygon still matches the nearest zone whose
// boundary passes within ~2 miles: spot coordinates sit on the sand or just
// offshore while zone polygons cover the coastal strip, so anything farther
// than that is a different stretch of coast. Expressed in degrees of
// latitude (2 mi / 69 mi per degree); longitudes are cos(lat)-scaled before
// comparing so the threshold holds east-west too.
const zoneNearDeg = 2.0 / 69.0

// parseZoneRings flattens a GeoJSON Polygon or MultiPolygon geometry into
// its rings ([lon, lat] positions).
func parseZoneRings(raw json.RawMessage) [][][]float64 {
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if json.Unmarshal(raw, &g) != nil {
		return nil
	}
	switch g.Type {
	case "Polygon":
		var p [][][]float64
		if json.Unmarshal(g.Coordinates, &p) != nil {
			return nil
		}
		return p
	case "MultiPolygon":
		var mp [][][][]float64
		if json.Unmarshal(g.Coordinates, &mp) != nil {
			return nil
		}
		var rings [][][]float64
		for _, p := range mp {
			rings = append(rings, p...)
		}
		return rings
	}
	return nil
}

// pointInRings is an even-odd ray cast across all rings, which also handles
// polygon holes.
func pointInRings(lat, lon float64, rings [][][]float64) bool {
	inside := false
	for _, ring := range rings {
		for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
			if len(ring[i]) < 2 || len(ring[j]) < 2 {
				continue
			}
			xi, yi := ring[i][0], ring[i][1]
			xj, yj := ring[j][0], ring[j][1]
			if (yi > lat) != (yj > lat) && lon < (xj-xi)*(lat-yi)/(yj-yi)+xi {
				inside = !inside
			}
		}
	}
	return inside
}

// minEdgeDist2 is the squared distance from the point to the nearest
// boundary segment, in degrees-of-latitude equivalent (longitude deltas are
// scaled by cos(lat)). Segment distance, not vertex distance: a boundary can
// run straight past a spot for miles without a vertex nearby.
func minEdgeDist2(lat, lon float64, rings [][][]float64) float64 {
	lonScale := math.Cos(lat * math.Pi / 180)
	px, py := lon*lonScale, lat
	best := math.MaxFloat64
	for _, ring := range rings {
		for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
			if len(ring[i]) < 2 || len(ring[j]) < 2 {
				continue
			}
			ax, ay := ring[j][0]*lonScale, ring[j][1]
			bx, by := ring[i][0]*lonScale, ring[i][1]
			abx, aby := bx-ax, by-ay
			t := 0.0
			if lenSq := abx*abx + aby*aby; lenSq > 0 {
				t = math.Max(0, math.Min(1, ((px-ax)*abx+(py-ay)*aby)/lenSq))
			}
			dx, dy := px-(ax+t*abx), py-(ay+t*aby)
			if d := dx*dx + dy*dy; d < best {
				best = d
			}
		}
	}
	return best
}

// zoneRings returns the parsed boundary of a zone, parsing and caching on
// first use (boundaries are static). A zone whose geometry fails to parse
// caches an empty slice so it isn't re-parsed every request.
func (s *SurfZoneStore) zoneRings(id string, geom json.RawMessage) [][][]float64 {
	s.mu.RLock()
	rings, ok := s.rings[id]
	s.mu.RUnlock()
	if ok {
		return rings
	}
	rings = parseZoneRings(geom)
	if rings == nil {
		rings = [][][]float64{}
	}
	s.mu.Lock()
	s.rings[id] = rings
	s.mu.Unlock()
	return rings
}

// ZoneForPoint returns the surf zone forecast covering (lat, lon): the zone
// whose polygon contains the point, else the closest zone whose boundary
// comes within zoneNearDeg. ok is false when no zone is close enough.
func (s *SurfZoneStore) ZoneForPoint(lat, lon float64) (SurfZoneForecast, bool) {
	if s == nil {
		return SurfZoneForecast{}, false
	}
	type candidate struct {
		id   string
		geom json.RawMessage
	}
	s.mu.RLock()
	candidates := make([]candidate, 0, len(s.byZone))
	for id := range s.byZone {
		if g, ok := s.geo[id]; ok {
			candidates = append(candidates, candidate{id: id, geom: g.Geometry})
		}
	}
	s.mu.RUnlock()
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })

	bestID := ""
	bestDist := math.MaxFloat64
	for _, cand := range candidates {
		rings := s.zoneRings(cand.id, cand.geom)
		if len(rings) == 0 {
			continue
		}
		if pointInRings(lat, lon, rings) {
			return s.Get(cand.id)
		}
		if d := minEdgeDist2(lat, lon, rings); d < bestDist {
			bestID, bestDist = cand.id, d
		}
	}
	if bestID != "" && bestDist <= zoneNearDeg*zoneNearDeg {
		return s.Get(bestID)
	}
	return SurfZoneForecast{}, false
}

// fetchZoneGeometry GETs one zone's GeoJSON from api.weather.gov and
// anchors the marker at its boundary centroid.
func fetchZoneGeometry(zoneID string) (surfZoneGeo, error) {
	req, err := http.NewRequest(http.MethodGet, surfZoneGeoURL+zoneID, nil)
	if err != nil {
		return surfZoneGeo{}, err
	}
	req.Header.Set("User-Agent", surfZoneUserAgent)
	req.Header.Set("Accept", "application/geo+json")
	resp, err := surfZoneClient.Do(req)
	if err != nil {
		return surfZoneGeo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return surfZoneGeo{}, errZoneNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return surfZoneGeo{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return surfZoneGeo{}, err
	}
	var doc struct {
		Geometry json.RawMessage `json:"geometry"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return surfZoneGeo{}, err
	}
	if len(doc.Geometry) == 0 || string(doc.Geometry) == "null" {
		return surfZoneGeo{}, fmt.Errorf("no geometry in response")
	}
	lat, lon, ok := geometryCentroid(doc.Geometry)
	if !ok {
		return surfZoneGeo{}, fmt.Errorf("could not compute centroid")
	}
	return surfZoneGeo{Lat: lat, Lon: lon, Geometry: doc.Geometry}, nil
}

// geometryCentroid returns the centroid of a GeoJSON Polygon or
// MultiPolygon (of its largest ring, for multi-part zones).
func geometryCentroid(geom json.RawMessage) (lat, lon float64, ok bool) {
	var head struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(geom, &head); err != nil {
		return 0, 0, false
	}
	var rings [][][2]float64
	switch head.Type {
	case "Polygon":
		var poly [][][2]float64
		if err := json.Unmarshal(head.Coordinates, &poly); err != nil || len(poly) == 0 {
			return 0, 0, false
		}
		rings = append(rings, poly[0]) // outer ring only
	case "MultiPolygon":
		var multi [][][][2]float64
		if err := json.Unmarshal(head.Coordinates, &multi); err != nil {
			return 0, 0, false
		}
		for _, poly := range multi {
			if len(poly) > 0 {
				rings = append(rings, poly[0])
			}
		}
	default:
		return 0, 0, false
	}

	best, bestArea := -1, 0.0
	for i, ring := range rings {
		if a := ringArea(ring); a > bestArea {
			best, bestArea = i, a
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	return ringCentroid(rings[best])
}

// ringArea is the absolute shoelace area of a ring in degree units — only
// used to pick the largest part, so no projection is needed.
func ringArea(ring [][2]float64) float64 {
	area := 0.0
	for i := 0; i < len(ring); i++ {
		j := (i + 1) % len(ring)
		area += ring[i][0]*ring[j][1] - ring[j][0]*ring[i][1]
	}
	if area < 0 {
		area = -area
	}
	return area / 2
}

func ringCentroid(ring [][2]float64) (lat, lon float64, ok bool) {
	if len(ring) == 0 {
		return 0, 0, false
	}
	var cx, cy, area float64
	for i := 0; i < len(ring); i++ {
		j := (i + 1) % len(ring)
		cross := ring[i][0]*ring[j][1] - ring[j][0]*ring[i][1]
		cx += (ring[i][0] + ring[j][0]) * cross
		cy += (ring[i][1] + ring[j][1]) * cross
		area += cross
	}
	area /= 2
	if area > -1e-12 && area < 1e-12 {
		// degenerate ring: fall back to the vertex mean
		var mx, my float64
		for _, p := range ring {
			mx += p[0]
			my += p[1]
		}
		return my / float64(len(ring)), mx / float64(len(ring)), true
	}
	return cy / (6 * area), cx / (6 * area), true
}

// fetchURL GETs a tgftp URL with If-Modified-Since support. A 304 returns
// ("", prevMod, nil) so callers can skip re-parsing.
func (s *SurfZoneStore) fetchURL(url, prevMod string) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	if prevMod != "" {
		req.Header.Set("If-Modified-Since", prevMod)
	}
	resp, err := surfZoneClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return "", prevMod, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	return string(body), resp.Header.Get("Last-Modified"), nil
}

// --- product parsing --------------------------------------------------

var (
	surfWMORe     = regexp.MustCompile(`^F[A-Z]{3}\d{2} ([A-Z]{4}) (\d{6})$`)
	surfExpiresRe = regexp.MustCompile(`^Expires:(\d{12})`)
	// UGC line: zone codes then a ddhhmm expiry, e.g. "CAZ349-350-161030-"
	surfUGCRe = regexp.MustCompile(`^[A-Z]{2}Z\d{3}[->][0-9A-Z->]*\d{6}-$`)
	// local issuance line inside a segment: "1131 AM PDT Wed Jul 15 2026"
	surfLocalTimeRe = regexp.MustCompile(`^\d{3,4} [AP]M `)
	surfPeriodRe    = regexp.MustCompile(`^\.([^.].*?)\.\.\.`)
	// "Rip Current Risk*.......Moderate." — key, dot fill, value
	surfFieldRe  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9'()/ *]*?)\.{3,}\s*(.*)$`)
	surfNumRe    = regexp.MustCompile(`\d+(?:\.\d+)?`)
	surfDDHHMMRe = regexp.MustCompile(`^\d{6}$`)
	surfDotsRe   = regexp.MustCompile(`\.{3,}`)
)

// resolveDDHHMM turns a WMO day-of-month timestamp into an absolute UTC
// time near now, handling month rollover in both directions.
func resolveDDHHMM(s string, now time.Time) time.Time {
	day, _ := strconv.Atoi(s[0:2])
	hour, _ := strconv.Atoi(s[2:4])
	min, _ := strconv.Atoi(s[4:6])
	t := time.Date(now.Year(), now.Month(), day, hour, min, 0, 0, time.UTC)
	if t.Sub(now) > 7*24*time.Hour {
		t = t.AddDate(0, -1, 0)
	} else if now.Sub(t) > 20*24*time.Hour {
		t = t.AddDate(0, 1, 0)
	}
	return t
}

// expandUGCZones expands the zone codes of a UGC line: "CAZ349-350-161030-"
// covers CAZ349 and CAZ350; ranges use '>' ("CAZ340>342").
func expandUGCZones(line string) []string {
	line = strings.TrimSuffix(line, "-")
	tokens := strings.Split(line, "-")
	if len(tokens) > 0 { // drop the trailing ddhhmm expiry
		if surfDDHHMMRe.MatchString(tokens[len(tokens)-1]) {
			tokens = tokens[:len(tokens)-1]
		}
	}
	var zones []string
	prefix := ""
	addRange := func(from, to int) {
		for n := from; n <= to; n++ {
			zones = append(zones, fmt.Sprintf("%s%03d", prefix, n))
		}
	}
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if len(tok) >= 3 && tok[0] >= 'A' && tok[0] <= 'Z' {
			prefix = tok[:3] // "CAZ349" -> prefix CAZ
			tok = tok[3:]
		}
		if prefix == "" || tok == "" {
			continue
		}
		if strings.Contains(tok, ">") {
			parts := strings.SplitN(tok, ">", 2)
			from, err1 := strconv.Atoi(parts[0])
			to, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil {
				addRange(from, to)
			}
			continue
		}
		if n, err := strconv.Atoi(tok); err == nil {
			addRange(n, n)
		}
	}
	return zones
}

// parseSurfFeet extracts a feet range from office wording: "2 to 4 feet",
// "Around 3 feet", "1 foot or less". Returns nils when no number is found
// (e.g. "Flat.").
func parseSurfFeet(v string) (*float64, *float64) {
	nums := surfNumRe.FindAllString(v, -1)
	if len(nums) == 0 {
		return nil, nil
	}
	min, max := 0.0, 0.0
	for i, n := range nums {
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			continue
		}
		if i == 0 || f < min {
			min = f
		}
		if i == 0 || f > max {
			max = f
		}
	}
	if strings.Contains(strings.ToLower(v), "or less") {
		min = 0
	}
	return &min, &max
}

func cleanFieldValue(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

// parseSurfZoneProduct parses one SRF text file into a forecast per zone it
// covers. The format is the NWS text-product skeleton: header (WMO line,
// UGC zone line, zone names), optional "...HEADLINE..." lines, then
// ".PERIOD..." sections of dot-filled fields until "&&" or "$$".
func parseSurfZoneProduct(text, state string, now time.Time) ([]SurfZoneForecast, error) {
	base := SurfZoneForecast{State: state, Raw: strings.ReplaceAll(text, "\r", "")}
	lines := strings.Split(base.Raw, "\n")

	var zoneIDs, zoneNames []string
	var period *SurfZonePeriod
	var periods []SurfZonePeriod
	var lastField string // key of the field continuation lines extend
	inBody := false      // between the segment's local time line and "&&"

	flushPeriod := func() {
		if period != nil {
			periods = append(periods, *period)
			period = nil
		}
		lastField = ""
	}

	setField := func(key, value string) {
		key = strings.TrimRight(strings.TrimSpace(key), "* ")
		value = cleanFieldValue(value)
		lastField = key
		switch key {
		case "Rip Current Risk":
			period.RipRisk = strings.TrimSuffix(value, ".")
		case "Surf Height":
			period.SurfHeight = strings.TrimSuffix(value, ".")
			period.SurfMinFt, period.SurfMaxFt = parseSurfFeet(value)
		case "Water Temperature":
			period.WaterTemp = strings.TrimSuffix(value, ".")
		case "Weather":
			period.Weather = value
		case "Winds":
			period.Winds = value
		case "Remarks":
			period.Remarks = value
		case "Tides":
			if value != "" {
				period.Tides = append(period.Tides, value)
			}
		default:
			if period.Fields == nil {
				period.Fields = map[string]string{}
			}
			period.Fields[key] = value
		}
	}

	appendToField := func(key, extra string) {
		extra = cleanFieldValue(extra)
		if extra == "" {
			return
		}
		switch key {
		case "Tides":
			// tide lines carry their own dot fill: "La Jolla.......Low -1.5 feet"
			period.Tides = append(period.Tides, surfDotsRe.ReplaceAllString(extra, ": "))
		case "Surf Height":
			period.SurfHeight = strings.TrimSuffix(period.SurfHeight+" "+extra, ".")
			period.SurfMinFt, period.SurfMaxFt = parseSurfFeet(period.SurfHeight)
		case "Rip Current Risk":
			period.RipRisk = strings.TrimSuffix(period.RipRisk+" "+extra, ".")
		case "Water Temperature":
			period.WaterTemp = strings.TrimSuffix(period.WaterTemp+" "+extra, ".")
		case "Weather":
			period.Weather += " " + extra
		case "Winds":
			period.Winds += " " + extra
		case "Remarks":
			period.Remarks += " " + extra
		default:
			if period.Fields != nil && period.Fields[key] != "" {
				period.Fields[key] += " " + extra
			}
		}
	}

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " ")
		trimmed := strings.TrimSpace(line)

		if m := surfExpiresRe.FindStringSubmatch(trimmed); m != nil {
			if t, err := time.Parse("200601021504", m[1]); err == nil {
				base.Expires = t
			}
			continue
		}
		if m := surfWMORe.FindStringSubmatch(trimmed); m != nil {
			base.Office = m[1]
			base.Issued = resolveDDHHMM(m[2], now)
			continue
		}
		if surfUGCRe.MatchString(trimmed) {
			zoneIDs = expandUGCZones(trimmed)
			continue
		}
		// zone name block: between the UGC line and the local time line
		if len(zoneIDs) > 0 && !inBody {
			if surfLocalTimeRe.MatchString(trimmed) {
				inBody = true
				continue
			}
			if strings.HasPrefix(trimmed, "Including") {
				base.Beaches = strings.TrimSuffix(trimmed, "-")
				continue
			}
			if strings.HasSuffix(trimmed, "-") && trimmed != "-" {
				zoneNames = append(zoneNames, strings.TrimSuffix(trimmed, "-"))
			}
			continue
		}
		if !inBody {
			continue
		}

		if trimmed == "&&" || trimmed == "$$" {
			flushPeriod()
			inBody = false // footer text (risk definitions) is not data
			continue
		}
		if strings.HasPrefix(trimmed, "...") {
			if period == nil {
				base.Headlines = append(base.Headlines, strings.Trim(trimmed, "."))
			}
			continue
		}
		if m := surfPeriodRe.FindStringSubmatch(trimmed); m != nil && !strings.HasPrefix(rawLine, " ") {
			flushPeriod()
			period = &SurfZonePeriod{Name: strings.TrimSpace(m[1])}
			continue
		}
		if period == nil {
			continue
		}
		// continuation lines are indented under their field
		if strings.HasPrefix(rawLine, " ") && lastField != "" {
			appendToField(lastField, trimmed)
			continue
		}
		if m := surfFieldRe.FindStringSubmatch(trimmed); m != nil {
			setField(m[1], m[2])
			continue
		}
		// "Tides..." with no fill dots to a value (Florida/New Jersey style)
		if strings.HasPrefix(trimmed, "Tides...") {
			setField("Tides", strings.TrimLeft(strings.TrimPrefix(trimmed, "Tides"), "."))
			continue
		}
	}
	flushPeriod()

	if len(zoneIDs) == 0 {
		return nil, fmt.Errorf("no UGC zone line found")
	}
	if len(periods) == 0 {
		return nil, fmt.Errorf("no forecast periods found")
	}
	base.Periods = periods

	out := make([]SurfZoneForecast, 0, len(zoneIDs))
	for i, id := range zoneIDs {
		f := base
		f.ZoneID = id
		switch {
		case i < len(zoneNames):
			f.ZoneName = zoneNames[i]
		case len(zoneNames) > 0:
			f.ZoneName = zoneNames[len(zoneNames)-1]
		default:
			f.ZoneName = id
		}
		out = append(out, f)
	}
	return out, nil
}

// --- API --------------------------------------------------------------

var surfZoneIDRe = regexp.MustCompile(`^[A-Za-z]{2}Z\d{3}$`)

type surfZoneSummary struct {
	ZoneID     string    `json:"zoneId"`
	ZoneName   string    `json:"zoneName"`
	Beaches    string    `json:"beaches,omitempty"`
	State      string    `json:"state"`
	Office     string    `json:"office"`
	Issued     time.Time `json:"issued"`
	Expires    time.Time `json:"expires"`
	Headlines  []string  `json:"headlines,omitempty"`
	PeriodName string    `json:"periodName"`
	RipRisk    string    `json:"ripRisk,omitempty"`
	SurfHeight string    `json:"surfHeight,omitempty"`
	SurfMinFt  *float64  `json:"surfMinFt,omitempty"`
	SurfMaxFt  *float64  `json:"surfMaxFt,omitempty"`
	WaterTemp  string    `json:"waterTemp,omitempty"`
	// marker anchor; absent until the zone's geometry has been fetched
	Lat *float64 `json:"lat,omitempty"`
	Lon *float64 `json:"lon,omitempty"`
}

// handleList serves a one-period summary for every cached zone.
func (s *SurfZoneStore) handleList(c *gin.Context) {
	s.mu.RLock()
	summaries := make([]surfZoneSummary, 0, len(s.byZone))
	for _, f := range s.byZone {
		sum := surfZoneSummary{
			ZoneID:    f.ZoneID,
			ZoneName:  f.ZoneName,
			Beaches:   f.Beaches,
			State:     f.State,
			Office:    f.Office,
			Issued:    f.Issued,
			Expires:   f.Expires,
			Headlines: f.Headlines,
		}
		if len(f.Periods) > 0 {
			p := f.Periods[0]
			sum.PeriodName = p.Name
			sum.RipRisk = p.RipRisk
			sum.SurfHeight = p.SurfHeight
			sum.SurfMinFt = p.SurfMinFt
			sum.SurfMaxFt = p.SurfMaxFt
			sum.WaterTemp = p.WaterTemp
		}
		if g, ok := s.geo[f.ZoneID]; ok {
			lat, lon := g.Lat, g.Lon
			sum.Lat, sum.Lon = &lat, &lon
		}
		summaries = append(summaries, sum)
	}
	s.mu.RUnlock()
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].ZoneID < summaries[j].ZoneID })
	c.Header("Cache-Control", "public, max-age=600")
	c.JSON(http.StatusOK, summaries)
}

// handleZone serves the full parsed forecast (periods + raw product text).
func (s *SurfZoneStore) handleZone(c *gin.Context) {
	zoneID := strings.ToUpper(c.Param("zoneId"))
	if !surfZoneIDRe.MatchString(zoneID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Unknown zone"})
		return
	}
	s.mu.RLock()
	f, ok := s.byZone[zoneID]
	if g, geoOK := s.geo[zoneID]; geoOK {
		f.Geometry = g.Geometry
	}
	s.mu.RUnlock()
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Unknown zone"})
		return
	}
	c.Header("Cache-Control", "public, max-age=600")
	c.JSON(http.StatusOK, f)
}
