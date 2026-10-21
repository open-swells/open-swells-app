package main

// The spot page's exposure map: a zoomed-in square of the app's basemap
// centered on the spot (drawn by MapLibre in the page), with the range of
// swell directions the beach is open to drawn over it as a wedge from the
// beach out, and the current swells as arrows in to the beach. A north-up Web Mercator map is conformal, so the overlay's screen
// angles are true bearings at any zoom.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SpotExposure is the exposure map's overlay; the page renders the map.
// Paths are in a 100x100 viewBox centered on the spot.
type SpotExposure struct {
	ArcPath string       // open directions
	Arrows  []swellArrow // current swells, largest first so none hides a smaller one
}

// swellArrow is a current swell train drawn in to the beach, the way the
// swell travels, sized by its height.
type swellArrow struct {
	Kind  string // "primary" or "secondary"
	Path  string // shaft and head, one path
	Scale float64
	Has   bool
}

// Arrow geometry, as radii from the spot in the viewBox, for a full-size
// arrow: the tail sits just inside the square's edge, the tip stops short
// of the beach dot.
const (
	arrowTail     = 46.0
	arrowTip      = 4.5
	arrowHeadLen  = 9.0
	arrowHeadHalf = 4.5
	arrowShaft    = 1.4 // half-width
)

// Arrows scale with swell height: full size at arrowFullFt and up, never
// below arrowMinScale so a small train still reads. Scale goes with the
// square root of height, so a 1ft train is not lost next to a 4ft one.
const (
	arrowFullFt   = 6.0
	arrowMinScale = 0.3
)

// arrowScale is the arrow size for a train heightFt tall, 0..1.
func arrowScale(heightFt float64) float64 {
	return math.Max(arrowMinScale, math.Min(1, math.Sqrt(heightFt/arrowFullFt)))
}

// newSwellArrow draws swell heightFt tall arriving from bearing deg (model
// strings such as "2.3" and "210"), false when there is no direction or
// height. The tip stays at the beach; length scales with height and width
// half as much, so small arrows stay solid enough to see.
func newSwellArrow(deg, heightFt string) swellArrow {
	d, err := strconv.ParseFloat(deg, 64)
	if err != nil {
		return swellArrow{}
	}
	h, err := strconv.ParseFloat(heightFt, 64)
	if err != nil {
		return swellArrow{}
	}
	length := arrowScale(h)
	width := (1 + length) / 2
	// offset(r, w) is the point at radius r along the bearing, w across it
	// (positive clockwise)
	rad := d * math.Pi / 180
	offset := func(r, w float64) string {
		x := 50 + r*math.Sin(rad) + w*math.Cos(rad)
		y := 50 - r*math.Cos(rad) + w*math.Sin(rad)
		return fmt.Sprintf("%.2f %.2f", x, y)
	}
	tail := arrowTip + (arrowTail-arrowTip)*length
	neck := arrowTip + arrowHeadLen*width
	shaft, head := arrowShaft*width, arrowHeadHalf*width
	return swellArrow{
		Path: fmt.Sprintf("M%s L%s L%s L%s L%s L%s L%s Z",
			offset(tail, -shaft), offset(tail, shaft),
			offset(neck, shaft), offset(neck, head),
			offset(arrowTip, 0),
			offset(neck, -head), offset(neck, -shaft)),
		Scale: length,
		Has:   true,
	}
}

// exposureRadius is how far the open-direction wedge reaches, in the arc's
// 100x100 viewBox: past the square's edges, so only the corners show its
// rim and the wedge reads as rays out from the beach.
const exposureRadius = 75.0

// arcPoint is bearing deg at radius r from the viewBox center, north up.
func arcPoint(deg, r float64) string {
	rad := deg * math.Pi / 180
	return fmt.Sprintf("%.2f %.2f", 50+r*math.Sin(rad), 50-r*math.Cos(rad))
}

// blocked reports whether land stands between the beach and swell from
// dir: outside the hand-tuned window or every open-water arc, or with only
// a facing, behind the beach.
func blocked(dir float64, b beach) bool {
	if len(b.Window) == 2 {
		return arcPast(dir, [2]float64{b.Window[0], b.Window[1]}) > 0
	}
	if len(b.OpenWater) > 0 {
		for _, arc := range b.OpenWater {
			if arcPast(dir, arc) == 0 {
				return false
			}
		}
		return true
	}
	return angDiff(dir, b.Facing) > 90
}

// circularRuns returns the [start, end] index ranges (inclusive, end may
// wrap past n-1) where in[i] holds, walking the circle once.
func circularRuns(in []bool) [][2]int {
	n := len(in)
	start := -1
	for i := range in {
		if !in[i] && in[(i+1)%n] {
			start = (i + 1) % n
			break
		}
	}
	if start == -1 { // all one value
		if n > 0 && in[0] {
			return [][2]int{{0, n - 1}}
		}
		return nil
	}
	var runs [][2]int
	for i := 0; i < n; i++ {
		j := (start + i) % n
		if !in[j] {
			continue
		}
		if len(runs) > 0 && runs[len(runs)-1][1] == start+i-1 {
			runs[len(runs)-1][1] = start + i
		} else {
			runs = append(runs, [2]int{start + i, start + i})
		}
	}
	return runs
}

// openArcPath draws each run of open directions as a wedge from the beach.
func openArcPath(b beach) string {
	open := make([]bool, 360)
	for d := range open {
		open[d] = !blocked(float64(d), b)
	}
	var path strings.Builder
	for _, run := range circularRuns(open) {
		// open degrees are inclusive, so a [170, 260] window spans 170–260
		from, to := float64(run[0]), float64(run[1])
		if to-from >= 359 { // open all round: a full disc
			fmt.Fprintf(&path, "M%s A%.0f %.0f 0 1 1 %s A%.0f %.0f 0 1 1 %s Z ",
				arcPoint(0, exposureRadius), exposureRadius, exposureRadius, arcPoint(180, exposureRadius),
				exposureRadius, exposureRadius, arcPoint(0, exposureRadius))
			continue
		}
		large := 0
		if to-from > 180 {
			large = 1
		}
		fmt.Fprintf(&path, "M50 50 L%s A%.0f %.0f 0 %d 1 %s Z ",
			arcPoint(from, exposureRadius), exposureRadius, exposureRadius, large, arcPoint(to, exposureRadius))
	}
	return strings.TrimSpace(path.String())
}

// spotExposure builds the exposure map's overlay, with the report's swell
// trains when there is one, false when nothing is known about which way
// the beach faces.
func spotExposure(b beach, report SpotReport, hasReport bool) (SpotExposure, bool) {
	if !b.HasFacing {
		return SpotExposure{}, false
	}
	e := SpotExposure{ArcPath: openArcPath(b)}
	if hasReport {
		primary := newSwellArrow(report.PrimaryDegrees, report.PrimaryHeight)
		primary.Kind = "primary"
		var secondary swellArrow
		if report.HasSecondary {
			secondary = newSwellArrow(report.SecondaryDegrees, report.SecondaryHeight)
			secondary.Kind = "secondary"
		}
		// Later arrows draw on top: the primary goes last unless the
		// secondary is smaller, which would vanish under it.
		order := []swellArrow{secondary, primary}
		if secondary.Scale < primary.Scale {
			order = []swellArrow{primary, secondary}
		}
		for _, a := range order {
			if a.Has {
				e.Arrows = append(e.Arrows, a)
			}
		}
	}
	return e, true
}
