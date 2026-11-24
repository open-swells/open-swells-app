package main

// Routes for the server-drawn images: spot and region share cards (Open
// Graph images) and the heatmap overlay for the spot page's exposure map.

import (
	"bytes"
	"fmt"
	"image/png"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

// imageCache holds encoded images by key. Keys carry the frame they were
// drawn from, so old entries are simply never asked for again; a full
// cache evicts one entry per insert. Concurrent misses for one key share
// a single render (a card render allocates ~25MB).
type imageCache struct {
	mu     sync.Mutex
	max    int
	data   map[string][]byte
	flight singleflight.Group
}

func newImageCache(max int) *imageCache {
	return &imageCache{max: max, data: map[string][]byte{}}
}

func (c *imageCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.data[key]
	return b, ok
}

func (c *imageCache) put(key string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.data {
		if len(c.data) < c.max {
			break
		}
		delete(c.data, k)
	}
	c.data[key] = b
}

// renderSlots caps simultaneous image renders. Each holds ~25MB of
// buffers and a core for 0.1-0.3s, and the app server has 1GB of RAM and
// one CPU: a burst of uncached cards (a crawler, a shared link unfurling
// in many places) waits its turn instead of exhausting memory.
var renderSlots = make(chan struct{}, 2)

func withRenderSlot(render func() ([]byte, error)) ([]byte, error) {
	renderSlots <- struct{}{}
	defer func() { <-renderSlots }()
	return render()
}

// getOrRender returns the cached image, rendering it once on a miss.
func (c *imageCache) getOrRender(key string, render func() ([]byte, error)) ([]byte, error) {
	if b, ok := c.get(key); ok {
		return b, nil
	}
	v, err, _ := c.flight.Do(key, func() (interface{}, error) {
		if b, ok := c.get(key); ok {
			return b, nil
		}
		b, err := withRenderSlot(render)
		if err == nil {
			c.put(key, b)
		}
		return b, err
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// serveFrameImage serves an image drawn from the current heatmap frame,
// cached under the frame it was drawn from. Without a frame (a publish in
// progress, no forecast yet) it draws one without the heatmap and
// serves it briefly, uncached, so the gap isn't pinned for an hour.
func serveFrameImage(c *gin.Context, staticDir string, cache *imageCache, name, contentType string, maxAge int, render func(frame *heatmapFrame) ([]byte, error)) {
	frame, err := currentHeatmapFrame(staticDir)
	var body []byte
	if err != nil {
		log.Printf("%s without heatmap: %v", strings.ReplaceAll(name, "\x00", " "), err)
		if body, err = withRenderSlot(func() ([]byte, error) { return render(nil) }); err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		maxAge = 60
	} else if body, err = cache.getOrRender(name+"\x00"+frame.Version, func() ([]byte, error) { return render(frame) }); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	c.Data(http.StatusOK, contentType, body)
}

var (
	cardCache    = newImageCache(400) // ~65KB each
	previewCache = newImageCache(800) // ~10KB each
	mapCache     = newImageCache(300) // ~120KB each
)

// imageVersion names the hour an image shows, for its URL: a link shared
// in a later hour gets a fresh image instead of a messenger's cached one.
// The publish time tells apart a rerun of the same model cycle.
func imageVersion(staticDir string, now time.Time) string {
	meta := currentHeatmapMeta(staticDir)
	hour, ok := meta.hourNear(now)
	if !ok {
		return ""
	}
	v := fmt.Sprintf("%s-%03d", meta.ForecastStart, hour)
	if t, ok := meta.Published(); ok {
		v += "-" + strconv.FormatInt(t.Unix(), 36)
	}
	return v
}

// --- Spot cards ------------------------------------------------------------

// SpotCardPath is the spot's share card URL path.
func SpotCardPath(staticDir, id string) string {
	path := "/surf-spots/" + id + "/card.jpg"
	if v := imageVersion(staticDir, time.Now()); v != "" {
		path += "?v=" + v
	}
	return path
}

func spotCardPanel(spot Spot, name string, snap SpotSnapshot, hasSnap bool, now time.Time) (cardPanel, *cardArrow) {
	p := cardPanel{Title: name, Subtitle: strings.Join(regionPath(spot.Region), ", ")}
	loc := spotLocation(spot)
	p.Footer = "Forecast for " + now.In(loc).Format("Mon Jan 2, 3 PM MST")
	var arrow *cardArrow
	if hasSnap && snap.HasNow() {
		p.Label = "SURF NOW"
		p.Big = surfFeet(snap.Surf)
		p.Rating = snap.Rating
		if swell := snap.swellPhrase(); swell != "" {
			p.Lines = append(p.Lines, "Swell "+swell)
		}
		if dir, err := strconv.ParseFloat(snap.SwellDir, 64); err == nil {
			arrow = &cardArrow{Lon: spot.Lon, Lat: spot.Lat, From: dir}
		}
		if dir, err := strconv.ParseFloat(snap.WindDir, 64); err == nil && snap.WindMph != "" {
			line := fmt.Sprintf("Wind %s mph from the %s", snap.WindMph, compassPoint(dir))
			p.Lines = append(p.Lines, line)
		}
	} else {
		p.Label = "16-DAY FORECAST"
		p.Big = "Swell & wind"
	}
	return p, arrow
}

func spotCardHandler(staticDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		spot, ok := spotStore.Get(c.Param("slug"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		serveFrameImage(c, staticDir, cardCache, "spot\x00"+spot.ID, "image/jpeg", 3600, func(frame *heatmapFrame) ([]byte, error) {
			now := time.Now()
			snap, hasSnap := spotSnapshots.Get(spot.ID)
			if !hasSnap {
				snap, hasSnap = buildSpotSnapshot(staticDir, spot, now)
			}
			panel, arrow := spotCardPanel(spot, spotStore.SEOName(spot), snap, hasSnap, now)
			view := viewAt(spot.Lon, spot.Lat, 8, cardW, cardH, (float64(cardPanelW)+float64(cardW-cardPanelW)/2)/cardW, 0.5)
			return renderCard(frame, view, panel, []cardMarker{{Lon: spot.Lon, Lat: spot.Lat, R: 9}}, arrow)
		})
	}
}

// --- Region cards ----------------------------------------------------------

func regionCardPath(staticDir string, n *RegionNode) string {
	path := n.URL() + "/card.jpg"
	if v := imageVersion(staticDir, time.Now()); v != "" {
		path += "?v=" + v
	}
	return path
}

func regionCard(frame *heatmapFrame, n *RegionNode) ([]byte, error) {
	spots := n.Descendants()
	p := cardPanel{
		Title:  n.Name,
		Label:  "SURF SPOTS",
		Big:    formatCount(len(spots)),
		Footer: "Free 16-day surf forecasts",
	}
	if anc := n.Ancestors(); len(anc) > 0 {
		var names []string
		for i := len(anc) - 1; i >= 0; i-- {
			names = append(names, anc[i].Name)
		}
		p.Subtitle = strings.Join(names, ", ")
	} else {
		p.Subtitle = "Surf forecast"
	}
	if best, ok := bestSnapshot(spots, spotSnapshots.Current()); ok {
		p.Lines = append(p.Lines, fmt.Sprintf("Best now: %s at %s", surfFeet(best.Snap.Surf), best.Spot.Name))
	}
	view := n.cardView()
	r := 6.0
	if len(spots) > 60 {
		r = 4
	}
	return renderCard(frame, view, p, regionMarkers(spots, view, r), nil)
}

type rankedSpot struct {
	Spot Spot
	Snap SpotSnapshot
}

// bestSnapshot is the spot rated highest now, preferring bigger surf on a
// tie.
func bestSnapshot(spots []Spot, snaps SnapshotSet) (rankedSpot, bool) {
	var best rankedSpot
	found := false
	for _, s := range spots {
		snap, ok := snaps.Get(s.ID)
		if !ok || !snap.HasNow() {
			continue
		}
		if !found || snap.Score > best.Snap.Score || (snap.Score == best.Snap.Score && snap.SurfHiFt > best.Snap.SurfHiFt) {
			best, found = rankedSpot{s, snap}, true
		}
	}
	return best, found
}

// --- Exposure map heatmap --------------------------------------------------

// previewSpanDeg is how much longitude the exposure map's heatmap covers:
// comfortably more than the map shows (about a fifth of a degree at its
// zoom), so it never runs out at the edges.
const (
	previewSpanDeg = 0.6
	previewPx      = 128
)

func spotPreviewView(spot Spot) mercView {
	return viewAt(spot.Lon, spot.Lat, previewSpanDeg, previewPx, previewPx, 0.5, 0.5)
}

// HeatmapPreview is what the page needs to overlay the image: its URL and
// corner coordinates, northwest clockwise, as MapLibre's image source
// takes them.
type HeatmapPreview struct {
	URL         string
	Coordinates [4][2]float64
}

func spotHeatmapPreview(staticDir string, spot Spot) (HeatmapPreview, bool) {
	v := imageVersion(staticDir, time.Now())
	if v == "" {
		return HeatmapPreview{}, false
	}
	view := spotPreviewView(spot)
	n, s := view.North(), view.South()
	round := func(x float64) float64 { return math.Round(x*1e6) / 1e6 }
	return HeatmapPreview{
		URL: "/surf-spots/" + spot.ID + "/heatmap.png?v=" + v,
		Coordinates: [4][2]float64{
			{round(view.X0), round(n)}, {round(view.X1), round(n)},
			{round(view.X1), round(s)}, {round(view.X0), round(s)},
		},
	}, true
}

func spotHeatmapHandler(staticDir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		spot, ok := spotStore.Get(c.Param("slug"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		frame, err := currentHeatmapFrame(staticDir)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		body, err := previewCache.getOrRender(spot.ID+"\x00"+frame.Version, func() ([]byte, error) {
			var buf bytes.Buffer
			enc := png.Encoder{CompressionLevel: png.BestSpeed}
			err := enc.Encode(&buf, frame.Render(spotPreviewView(spot), true))
			return buf.Bytes(), err
		})
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		// A versioned URL for the current hour never changes; anything
		// else (an old page asking for a past hour) gets the current
		// frame, briefly.
		if c.Query("v") == imageVersion(staticDir, time.Now()) {
			c.Header("Cache-Control", "public, max-age=86400")
		} else {
			c.Header("Cache-Control", "public, max-age=300")
		}
		c.Data(http.StatusOK, "image/png", body)
	}
}

// --- Region page maps -----------------------------------------------------

// Region page maps are drawn at twice the size they show at.
const (
	regionMapW = 1600
	regionMapH = 560
)

func regionMapPath(staticDir string, n *RegionNode) string {
	path := n.URL() + "/map.jpg"
	if v := imageVersion(staticDir, time.Now()); v != "" {
		path += "?v=" + v
	}
	return path
}

// regionMap is the region's spots on the current swell heatmap.
func regionMap(frame *heatmapFrame, n *RegionNode) ([]byte, error) {
	spots := n.Descendants()
	view := n.mapView(regionMapW, regionMapH, 0)
	r := 8.0
	switch {
	case len(spots) > 1000:
		r = 3.5
	case len(spots) > 60:
		r = 5
	}
	return renderMap(frame, view, regionMarkers(spots, view, r))
}

// regionMarkers places spots in view, shifting each by a world when that
// brings it into the view (regions across the antimeridian).
func regionMarkers(spots []Spot, view mercView, r float64) []cardMarker {
	markers := make([]cardMarker, 0, len(spots))
	for _, s := range spots {
		lon := s.Lon
		for lon < view.X0 && lon+360 <= view.X1+180 {
			lon += 360
		}
		for lon > view.X1 && lon-360 >= view.X0-180 {
			lon -= 360
		}
		markers = append(markers, cardMarker{Lon: lon, Lat: s.Lat, R: r})
	}
	return markers
}
