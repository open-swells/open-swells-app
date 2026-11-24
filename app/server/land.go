package main

// Land polygons for server-drawn maps (share cards). The heatmap has no
// coastline of its own (land cells are filled from nearby water, see
// render_heatmap_png in the pipeline), so the cards draw Natural Earth's
// 1:10m land over it, as the browser map draws its basemap's land mask.
// app/ops/build-land.py writes the file and documents its format.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"os"

	"golang.org/x/image/vector"
)

const landMagic = "OSLAND1\n"

// landRing is one polygon ring: lon/lat pairs and their bounding box.
type landRing struct {
	minLon, maxLon, minLat, maxLat float64
	pts                            []float32 // lon0, lat0, lon1, lat1, ...
}

type LandStore struct {
	rings []landRing
}

var landStore *LandStore

func LoadLandStore(path string) (*LandStore, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return parseLand(raw)
}

func parseLand(raw []byte) (*LandStore, error) {
	if !bytes.HasPrefix(raw, []byte(landMagic)) {
		return nil, errors.New("not a land file")
	}
	r := bufio.NewReader(bytes.NewReader(raw[len(landMagic):]))
	count, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	store := &LandStore{rings: make([]landRing, 0, count)}
	for i := uint64(0); i < count; i++ {
		n, err := binary.ReadUvarint(r)
		if err != nil {
			return nil, err
		}
		ring := landRing{minLon: 180, maxLon: -180, minLat: 90, maxLat: -90, pts: make([]float32, 0, 2*n)}
		var x, y int64
		for j := uint64(0); j < n; j++ {
			dx, err := binary.ReadVarint(r)
			if err != nil {
				return nil, err
			}
			dy, err := binary.ReadVarint(r)
			if err != nil {
				return nil, err
			}
			x, y = x+dx, y+dy
			lon, lat := float64(x)/1e4, float64(y)/1e4
			ring.minLon, ring.maxLon = math.Min(ring.minLon, lon), math.Max(ring.maxLon, lon)
			ring.minLat, ring.maxLat = math.Min(ring.minLat, lat), math.Max(ring.maxLat, lat)
			ring.pts = append(ring.pts, float32(lon), float32(lat))
		}
		store.rings = append(store.rings, ring)
	}
	return store, nil
}

// Draw fills the land inside view onto dst in col. Rings near the view's
// edges are drawn a world to either side as well, so a view across the
// antimeridian still gets both halves.
func (s *LandStore) Draw(dst draw.Image, v mercView, col color.Color) {
	if s == nil {
		return
	}
	north, south := v.North(), v.South()
	r := vector.NewRasterizer(v.W, v.H)
	var pts, clipped [][2]float64
	for _, ring := range s.rings {
		if ring.maxLat < south || ring.minLat > north {
			continue
		}
		for _, shift := range []float64{-360, 0, 360} {
			if ring.maxLon+shift < v.X0 || ring.minLon+shift > v.X1 {
				continue
			}
			pts = pts[:0]
			for i := 0; i < len(ring.pts); i += 2 {
				x, y := v.Pixel(float64(ring.pts[i])+shift, float64(ring.pts[i+1]))
				pts = append(pts, [2]float64{x, y})
			}
			// The rasterizer mishandles edges far outside its canvas (a
			// continent's ring reaches thousands of pixels off it), so
			// rings are clipped to just beyond the canvas first.
			clipped = clipToRect(pts, clipped[:0], -2, -2, float64(v.W)+2, float64(v.H)+2)
			if len(clipped) < 3 {
				continue
			}
			r.MoveTo(float32(clipped[0][0]), float32(clipped[0][1]))
			for _, p := range clipped[1:] {
				r.LineTo(float32(p[0]), float32(p[1]))
			}
			r.ClosePath()
		}
	}
	r.Draw(dst, image.Rect(0, 0, v.W, v.H), image.NewUniform(col), image.Point{})
}

// clipToRect clips a closed polygon to a rectangle (Sutherland-Hodgman).
// Parts outside collapse onto the rectangle's edges, which fills the same.
func clipToRect(in, out [][2]float64, x0, y0, x1, y1 float64) [][2]float64 {
	edges := []struct {
		inside func(p [2]float64) bool
		cross  func(a, b [2]float64) [2]float64
	}{
		{func(p [2]float64) bool { return p[0] >= x0 }, func(a, b [2]float64) [2]float64 { return lerpAt(a, b, (x0-a[0])/(b[0]-a[0])) }},
		{func(p [2]float64) bool { return p[0] <= x1 }, func(a, b [2]float64) [2]float64 { return lerpAt(a, b, (x1-a[0])/(b[0]-a[0])) }},
		{func(p [2]float64) bool { return p[1] >= y0 }, func(a, b [2]float64) [2]float64 { return lerpAt(a, b, (y0-a[1])/(b[1]-a[1])) }},
		{func(p [2]float64) bool { return p[1] <= y1 }, func(a, b [2]float64) [2]float64 { return lerpAt(a, b, (y1-a[1])/(b[1]-a[1])) }},
	}
	cur := append([][2]float64(nil), in...)
	for _, e := range edges {
		out = out[:0]
		for i, p := range cur {
			prev := cur[(i+len(cur)-1)%len(cur)]
			switch pin, qin := e.inside(p), e.inside(prev); {
			case pin && qin:
				out = append(out, p)
			case pin:
				out = append(out, e.cross(prev, p), p)
			case qin:
				out = append(out, e.cross(prev, p))
			}
		}
		if len(out) == 0 {
			return out
		}
		cur = append(cur[:0], out...)
	}
	return out
}

func lerpAt(a, b [2]float64, t float64) [2]float64 {
	return [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t}
}

// --- Web Mercator views ----------------------------------------------------

// maxMercLat is where the views clamp latitude (the heatmap's own limit).
const maxMercLat = 85.0

// mercY is a latitude's Web Mercator y, in degrees (so it shares the scale
// of longitude: one degree of x is one degree of y on screen).
func mercY(lat float64) float64 {
	lat = math.Max(-maxMercLat, math.Min(maxMercLat, lat))
	return math.Log(math.Tan(math.Pi/4+lat*math.Pi/360)) * 180 / math.Pi
}

func invMercY(y float64) float64 {
	return (2*math.Atan(math.Exp(y*math.Pi/180)) - math.Pi/2) * 180 / math.Pi
}

// mercView is a W x H pixel window onto Web Mercator: longitudes X0..X1
// left to right (unwrapped: they may run past ±180), Mercator y Y0 (top)
// to Y1 (bottom), in degrees.
type mercView struct {
	W, H   int
	X0, X1 float64
	Y0, Y1 float64
}

// viewAt is a view lonSpan degrees wide with (lon, lat) at pixel
// (fx*W, fy*H). Pixels are square, so the latitude range follows.
func viewAt(lon, lat, lonSpan float64, w, h int, fx, fy float64) mercView {
	perPx := lonSpan / float64(w)
	cy := mercY(lat)
	// Keep the window inside the Mercator limits.
	top := cy + fy*float64(h)*perPx
	limit := mercY(maxMercLat)
	if top > limit {
		cy -= top - limit
	}
	if bottom := cy - (1-fy)*float64(h)*perPx; bottom < -limit {
		cy += -limit - bottom
	}
	return mercView{
		W: w, H: h,
		X0: lon - fx*lonSpan, X1: lon + (1-fx)*lonSpan,
		Y0: cy + fy*float64(h)*perPx, Y1: cy - (1-fy)*float64(h)*perPx,
	}
}

// Pixel is where (lon, lat) falls in the view, in pixels.
func (v mercView) Pixel(lon, lat float64) (x, y float64) {
	return (lon - v.X0) / (v.X1 - v.X0) * float64(v.W), (v.Y0 - mercY(lat)) / (v.Y0 - v.Y1) * float64(v.H)
}

// LonAt and LatAt are the coordinates at a pixel position.
func (v mercView) LonAt(x float64) float64 { return v.X0 + x/float64(v.W)*(v.X1-v.X0) }
func (v mercView) LatAt(y float64) float64 { return invMercY(v.Y0 - y/float64(v.H)*(v.Y0-v.Y1)) }

func (v mercView) North() float64 { return invMercY(v.Y0) }
func (v mercView) South() float64 { return invMercY(v.Y1) }
