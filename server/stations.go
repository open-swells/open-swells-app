package main

// Buoy discovery: the map's station list is refreshed from NDBC instead of
// being frozen in the binary, so stations that stop reporting drop off the
// map and new deployments appear without a release.
//
//   - activestations.xml is NDBC's registry of every station: id, lat, lon,
//     and name. Positions move between deployments, so coordinates are
//     taken from here on every refresh.
//   - The realtime2 directory listing shows one {ID}.spec file per station
//     reporting spectral wave summaries (the data this app charts). Dead
//     stations' files linger for ~45 days, so a station only counts as live
//     when its .spec mtime is recent.
//
// Stations that vanish are marked inactive rather than deleted so names on
// existing favorites keep resolving. On a new database the startup refresh
// populates the table directly from NDBC.

import (
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	activeStationsURL  = "https://www.ndbc.noaa.gov/activestations.xml"
	realtimeListingURL = "https://www.ndbc.noaa.gov/data/realtime2/"

	stationFreshWindow  = 48 * time.Hour
	stationRefreshEvery = 24 * time.Hour

	// A partial NDBC response must never wipe the list: skip the refresh
	// when it yields implausibly few live spectral stations (~240 expected).
	minPlausibleStations = 100
)

// The realtime2 listing is a few MB of HTML; give it more room than the
// per-station report client.
var stationsClient = &http.Client{Timeout: 60 * time.Second}

var stationStore *StationStore

type BuoyLocation struct {
	StationID string  `json:"stationId"`
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type stationRecord struct {
	BuoyLocation
	Active   bool
	LastSeen time.Time
}

type StationStore struct {
	db *sql.DB

	mu     sync.RWMutex
	byID   map[string]stationRecord // every known station, active or not
	active []BuoyLocation           // active only, sorted by station id
}

func NewStationStore(db *sql.DB) *StationStore {
	return &StationStore{db: db, byID: map[string]stationRecord{}}
}

// Load populates the in-memory maps from the buoys table. RunRefresher performs
// an immediate NDBC refresh at startup and then refreshes the list daily.
func (s *StationStore) Load() error {
	_, err := s.loadFromDB()
	return err
}

func (s *StationStore) loadFromDB() (int, error) {
	rows, err := s.db.Query(`SELECT station_id, name, latitude, longitude, active, last_seen FROM buoys`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	byID := map[string]stationRecord{}
	var active []BuoyLocation
	for rows.Next() {
		var rec stationRecord
		var lastSeen string
		if err := rows.Scan(&rec.StationID, &rec.Name, &rec.Latitude, &rec.Longitude, &rec.Active, &lastSeen); err != nil {
			return 0, err
		}
		rec.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
		byID[rec.StationID] = rec
		if rec.Active {
			active = append(active, rec.BuoyLocation)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	sort.Slice(active, func(i, j int) bool { return active[i].StationID < active[j].StationID })

	s.mu.Lock()
	s.byID = byID
	s.active = active
	s.mu.Unlock()
	return len(byID), nil
}

// Has reports whether the station is known (active or not), so report and
// forecast pages keep working for favorites whose buoy went quiet.
func (s *StationStore) Has(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.byID[id]
	return ok
}

func (s *StationStore) Location(id string) (BuoyLocation, bool) {
	s.mu.RLock()
	rec, ok := s.byID[id]
	s.mu.RUnlock()
	return rec.BuoyLocation, ok
}

func (s *StationStore) DisplayName(id string) string {
	s.mu.RLock()
	rec, ok := s.byID[id]
	s.mu.RUnlock()
	if ok && rec.Name != "" {
		return rec.Name
	}
	return fmt.Sprintf("Buoy %s", id)
}

// OfflineSince returns when a station last reported, for stations that are
// no longer active. ok is false for active or unknown stations. last_seen
// for never-refreshed seed rows is the seed time, so the date is a floor,
// not an exact outage start.
func (s *StationStore) OfflineSince(id string) (time.Time, bool) {
	s.mu.RLock()
	rec, ok := s.byID[id]
	s.mu.RUnlock()
	if !ok || rec.Active {
		return time.Time{}, false
	}
	return rec.LastSeen, true
}

// RunRefresher refreshes once at startup and then daily. Run as a goroutine.
func (s *StationStore) RunRefresher() {
	for {
		if err := s.refresh(); err != nil {
			log.Printf("station refresh failed, keeping previous list: %v", err)
		}
		time.Sleep(stationRefreshEvery)
	}
}

func (s *StationStore) refresh() error {
	meta, err := fetchActiveStations()
	if err != nil {
		return fmt.Errorf("activestations.xml: %w", err)
	}
	fresh, err := fetchFreshSpectralIDs(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("realtime2 listing: %w", err)
	}

	var live []BuoyLocation
	for id := range fresh {
		if b, ok := meta[id]; ok {
			live = append(live, b)
		}
	}
	if len(live) < minPlausibleStations {
		return fmt.Errorf("only %d live spectral stations found, refusing to update", len(live))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE buoys SET active = 0`); err != nil {
		return err
	}
	for _, b := range live {
		if _, err := tx.Exec(
			`INSERT INTO buoys (station_id, name, latitude, longitude, active, last_seen, updated_at)
			 VALUES (?, ?, ?, ?, 1, ?, ?)
			 ON CONFLICT(station_id) DO UPDATE SET
			     name = excluded.name,
			     latitude = excluded.latitude,
			     longitude = excluded.longitude,
			     active = 1,
			     last_seen = excluded.last_seen,
			     updated_at = excluded.updated_at`,
			b.StationID, b.Name, b.Latitude, b.Longitude, now, now,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if _, err := s.loadFromDB(); err != nil {
		return err
	}
	log.Printf("station refresh: %d live spectral stations (%d known total)", len(live), len(s.byID))
	return nil
}

// handleList serves the active station list for the map frontend.
func (s *StationStore) handleList(c *gin.Context) {
	s.mu.RLock()
	active := s.active
	s.mu.RUnlock()
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, active)
}

func fetchActiveStations() (map[string]BuoyLocation, error) {
	resp, err := stationsClient.Get(activeStationsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var doc struct {
		Stations []struct {
			ID   string  `xml:"id,attr"`
			Lat  float64 `xml:"lat,attr"`
			Lon  float64 `xml:"lon,attr"`
			Name string  `xml:"name,attr"`
		} `xml:"station"`
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}

	stations := make(map[string]BuoyLocation, len(doc.Stations))
	for _, st := range doc.Stations {
		id := strings.ToUpper(strings.TrimSpace(st.ID))
		if id == "" {
			continue
		}
		name := strings.TrimSpace(st.Name)
		if name == "" {
			name = fmt.Sprintf("Buoy %s", id)
		}
		stations[id] = BuoyLocation{StationID: id, Name: name, Latitude: st.Lat, Longitude: st.Lon}
	}
	return stations, nil
}

// One listing row looks like:
//
//	<td><a href="41001.spec">41001.spec</a></td><td align="right">2026-07-11 17:40  </td>
var specListingRe = regexp.MustCompile(
	`href="([A-Z0-9]+)\.spec">[^<]*</a></td><td[^>]*>\s*(\d{4}-\d{2}-\d{2} \d{2}:\d{2})`)

// fetchFreshSpectralIDs returns the stations whose realtime .spec file was
// modified within stationFreshWindow. Listing mtimes are UTC.
func fetchFreshSpectralIDs(now time.Time) (map[string]bool, error) {
	resp, err := stationsClient.Get(realtimeListingURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	fresh := map[string]bool{}
	for _, m := range specListingRe.FindAllStringSubmatch(string(body), -1) {
		mtime, err := time.ParseInLocation("2006-01-02 15:04", m[2], time.UTC)
		if err != nil {
			continue
		}
		if now.Sub(mtime) < stationFreshWindow {
			fresh[m[1]] = true
		}
	}
	return fresh, nil
}
