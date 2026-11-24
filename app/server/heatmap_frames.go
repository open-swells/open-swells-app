package main

// Server-side heatmap rendering: the pipeline's global swell-height frames
// (heatmap_<HHH>.webp) and nearshore NWPS mosaics (nwps_<grid>_<HHH>.png),
// sampled into any Mercator view. The spot page's exposure map overlays a
// small crop, and the share cards draw a regional one.
//
// Both image kinds are stretched linearly in Web Mercator between corner
// coordinates from metadata.json (the pipeline resamples rows to Mercator
// spacing), so a pixel's position is linear in lon and in mercY.

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/webp"
)

type lonLatBounds struct {
	West  float64 `json:"west"`
	East  float64 `json:"east"`
	North float64 `json:"north"`
	South float64 `json:"south"`
}

// heatmapMeta is what the renderers need from metadata.json.
type heatmapMeta struct {
	ForecastStart string        `json:"forecast_start"`
	Timestamp     string        `json:"timestamp"`
	Hours         []int         `json:"hours"`
	HeatmapBounds *lonLatBounds `json:"heatmap_bounds"`
	NwpsLayers    []struct {
		Grid   string       `json:"grid"`
		Bounds lonLatBounds `json:"bounds"`
		Hours  []int        `json:"hours"`
	} `json:"nwps_layers"`
}

var (
	heatmapMetaMu      sync.Mutex
	heatmapMetaDir     string
	heatmapMetaModTime int64
	heatmapMetaValue   heatmapMeta
)

// currentHeatmapMeta parses metadata.json, again only when it changes.
func currentHeatmapMeta(staticDir string) heatmapMeta {
	path := filepath.Join(staticDir, "metadata.json")
	info, err := os.Stat(path)
	if err != nil {
		return heatmapMeta{}
	}
	heatmapMetaMu.Lock()
	defer heatmapMetaMu.Unlock()
	settled := time.Since(info.ModTime()) > metadataSettle
	if settled && heatmapMetaDir == staticDir && heatmapMetaModTime == info.ModTime().UnixNano() {
		return heatmapMetaValue
	}
	var value heatmapMeta
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, &value)
	}
	heatmapMetaDir, heatmapMetaModTime, heatmapMetaValue = staticDir, info.ModTime().UnixNano(), value
	return value
}

// Published is when the run's files were written: metadata's timestamp,
// else the run's start. False when neither parses.
func (m heatmapMeta) Published() (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, m.Timestamp); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("20060102_15Z", m.ForecastStart); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// hourNear is the run's frame hour valid closest to t.
func (m heatmapMeta) hourNear(t time.Time) (int, bool) {
	start, err := time.Parse("20060102_15Z", m.ForecastStart)
	if err != nil || len(m.Hours) == 0 {
		return 0, false
	}
	want := t.Sub(start).Hours()
	best := m.Hours[0]
	for _, h := range m.Hours {
		if math.Abs(float64(h)-want) < math.Abs(float64(best)-want) {
			best = h
		}
	}
	return best, true
}

// heatmapLayer is one decoded frame placed on the map.
type heatmapLayer struct {
	img    *image.NRGBA
	bounds lonLatBounds
	yTop   float64 // mercY(north)
	yBot   float64
	global bool // wraps around in longitude
}

// heatmapFrame is the global frame and the NWPS mosaics for one hour,
// drawn in that order.
type heatmapFrame struct {
	Run   string
	Hour  int
	Valid time.Time
	// Version identifies the pixels: the run, the hour and each file's
	// modification time, so an image cached from it never outlives a
	// republished file of the same name.
	Version string
	layers  []heatmapLayer
}

type decodedImage struct {
	modTime int64
	img     *image.NRGBA
}

var (
	frameImagesMu sync.Mutex
	frameImages   = map[string]decodedImage{}
	// frameDecode makes concurrent requests for a new frame share one
	// decode (a global frame is ~18MB decoded and takes ~80ms).
	frameDecodeMu sync.Mutex
)

// maxFrameImages bounds the decoded-image cache: a global frame and a few
// nearshore mosaics for the current hour, plus the hour before it.
const maxFrameImages = 8

// loadFrameImage decodes the image at path, with its modification time.
func loadFrameImage(path string) (*image.NRGBA, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	mod := info.ModTime().UnixNano()
	frameImagesMu.Lock()
	if d, ok := frameImages[path]; ok && d.modTime == mod {
		frameImagesMu.Unlock()
		return d.img, mod, nil
	}
	frameImagesMu.Unlock()

	frameDecodeMu.Lock()
	defer frameDecodeMu.Unlock()
	frameImagesMu.Lock()
	if d, ok := frameImages[path]; ok && d.modTime == mod {
		frameImagesMu.Unlock()
		return d.img, mod, nil
	}
	frameImagesMu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var decoded image.Image
	if strings.HasSuffix(path, ".webp") {
		decoded, err = webp.Decode(f)
	} else {
		decoded, err = png.Decode(f)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	img := toNRGBA(decoded)

	frameImagesMu.Lock()
	defer frameImagesMu.Unlock()
	if len(frameImages) >= maxFrameImages {
		for k := range frameImages {
			delete(frameImages, k)
			if len(frameImages) < maxFrameImages/2 {
				break
			}
		}
	}
	frameImages[path] = decodedImage{modTime: mod, img: img}
	return img, mod, nil
}

func toNRGBA(src image.Image) *image.NRGBA {
	if img, ok := src.(*image.NRGBA); ok && img.Rect.Min == (image.Point{}) {
		return img
	}
	b := src.Bounds()
	img := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			img.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return img
}

func newHeatmapLayer(img *image.NRGBA, b lonLatBounds, global bool) heatmapLayer {
	return heatmapLayer{img: img, bounds: b, yTop: mercY(b.North), yBot: mercY(b.South), global: global}
}

// currentHeatmapFrame is the frame valid closest to now.
func currentHeatmapFrame(staticDir string) (*heatmapFrame, error) {
	return heatmapFrameAt(staticDir, time.Now())
}

func heatmapFrameAt(staticDir string, t time.Time) (*heatmapFrame, error) {
	meta := currentHeatmapMeta(staticDir)
	hour, ok := meta.hourNear(t)
	if !ok || meta.HeatmapBounds == nil {
		return nil, fmt.Errorf("no heatmap frames in %s", staticDir)
	}
	start, _ := time.Parse("20060102_15Z", meta.ForecastStart)
	frame := &heatmapFrame{Run: meta.ForecastStart, Hour: hour, Valid: start.Add(time.Duration(hour) * time.Hour)}
	global, mod, err := loadFrameImage(filepath.Join(staticDir, fmt.Sprintf("heatmap_%03d.webp", hour)))
	if err != nil {
		return nil, err
	}
	version := fmt.Sprintf("%s-%03d-%x", meta.ForecastStart, hour, mod)
	b := *meta.HeatmapBounds
	frame.layers = append(frame.layers, newHeatmapLayer(global, b, b.East-b.West > 300))
	for _, layer := range meta.NwpsLayers {
		if !containsInt(layer.Hours, hour) {
			continue
		}
		img, mod, err := loadFrameImage(filepath.Join(staticDir, fmt.Sprintf("nwps_%s_%03d.png", layer.Grid, hour)))
		if err != nil {
			continue // the global frame still covers it
		}
		version += fmt.Sprintf("-%x", mod)
		frame.layers = append(frame.layers, newHeatmapLayer(img, layer.Bounds, false))
	}
	frame.Version = version
	return frame, nil
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Render samples the frame into view: premultiplied-alpha bilinear
// sampling of each layer, composited bottom to top. Pixels no layer covers
// stay transparent. nearshore adds the NWPS mosaics, which only show at
// coastal zooms: at regional scale their edges against the global frame
// read as boxes.
func (f *heatmapFrame) Render(v mercView, nearshore bool) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, v.W, v.H))
	acc := make([]float64, 4*v.W*v.H)
	for i, layer := range f.layers {
		if i > 0 && !nearshore {
			break
		}
		layer.composite(acc, v)
	}
	for i := 0; i < len(acc); i += 4 {
		out.Pix[i] = uint8(math.Round(acc[i] * 255))
		out.Pix[i+1] = uint8(math.Round(acc[i+1] * 255))
		out.Pix[i+2] = uint8(math.Round(acc[i+2] * 255))
		out.Pix[i+3] = uint8(math.Round(acc[i+3] * 255))
	}
	return out
}

// composite draws the layer over acc (premultiplied RGBA, 0..1).
//
// The bounds are the first and last samples' coordinates (the pipeline
// reports its grid's end points), so pixel i sits at West + i*step. The
// global frame's columns go once round the world: one more step past the
// last column is the first again.
func (l heatmapLayer) composite(acc []float64, v mercView) {
	w, h := l.img.Rect.Dx(), l.img.Rect.Dy()
	step := (l.bounds.East - l.bounds.West) / float64(w-1)
	cols := make([]float64, v.W)
	for x := range cols {
		lon := v.LonAt(float64(x) + 0.5)
		lon = l.bounds.West + math.Mod(math.Mod(lon-l.bounds.West, 360)+360, 360)
		cols[x] = (lon - l.bounds.West) / step
	}
	for y := 0; y < v.H; y++ {
		my := mercY(v.LatAt(float64(y) + 0.5))
		fy := (l.yTop - my) / (l.yTop - l.yBot) * float64(h-1)
		if fy < -0.5 || fy > float64(h)-0.5 {
			continue
		}
		for x := 0; x < v.W; x++ {
			fx := cols[x]
			if !l.global && (fx < -0.5 || fx > float64(w)-0.5) {
				continue
			}
			r, g, b, a := l.sample(fx, fy, w, h)
			if a == 0 {
				continue
			}
			i := 4 * (y*v.W + x)
			inv := 1 - a
			acc[i] = r + acc[i]*inv
			acc[i+1] = g + acc[i+1]*inv
			acc[i+2] = b + acc[i+2]*inv
			acc[i+3] = a + acc[i+3]*inv
		}
	}
}

// sample is the premultiplied bilinear color at fractional pixel (fx, fy).
func (l heatmapLayer) sample(fx, fy float64, w, h int) (r, g, b, a float64) {
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	tx, ty := fx-float64(x0), fy-float64(y0)
	for _, c := range [4]struct {
		dx, dy int
		wt     float64
	}{{0, 0, (1 - tx) * (1 - ty)}, {1, 0, tx * (1 - ty)}, {0, 1, (1 - tx) * ty}, {1, 1, tx * ty}} {
		if c.wt == 0 {
			continue
		}
		x, y := x0+c.dx, y0+c.dy
		if l.global {
			x = ((x % w) + w) % w
		} else {
			x = max(0, min(w-1, x))
		}
		y = max(0, min(h-1, y))
		p := l.img.Pix[y*l.img.Stride+4*x:]
		pa := float64(p[3]) / 255 * c.wt
		r += float64(p[0]) / 255 * pa
		g += float64(p[1]) / 255 * pa
		b += float64(p[2]) / 255 * pa
		a += pa
	}
	return
}
