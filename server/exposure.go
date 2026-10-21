package main

// The spot page's exposure dial: a top-down zoom on the beach showing the
// land around it, the swell directions it accepts (sampled from the same
// swellExposure the classifier uses, so the picture is the model), and
// where the current swells arrive from.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SpotExposure is the exposure card's data. Paths are drawn in the report
// compass's 40x40 viewBox, north up, with the beach at the center.
type SpotExposure struct {
	Facing     string // "SW 225°"
	FacingDeg  float64
	Accepts    string // accepted swell directions, e.g. "169–347°"
	LandPath   string // blocked directions, as wedges to the rim
	AcceptPath string // radius = share of swell height that gets in
	Primary    exposedSwell
	Secondary  exposedSwell
}

// exposedSwell is one current swell train against the beach.
type exposedSwell struct {
	Deg float64
	Pct int // share of the train's height that reaches the beach
	Has bool
}

const (
	dialCenter    = 20.0
	dialRim       = 17.0 // land and acceptance reach this radius
	dialMinRadius = 2.5  // a fully blocked direction
	// acceptedMin is the exposure below which a direction is not counted
	// as accepted in the label (the open-water taper's last few percent).
	acceptedMin = 0.05
)

// dialPoint is bearing deg at radius r, north up.
func dialPoint(deg, r float64) string {
	rad := deg * math.Pi / 180
	return fmt.Sprintf("%.1f %.1f", dialCenter+r*math.Sin(rad), dialCenter-r*math.Cos(rad))
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

// spotExposure builds the dial, false when nothing is known about which
// way the beach faces.
func spotExposure(b beach, report SpotReport, hasReport bool) (SpotExposure, bool) {
	if !b.HasFacing {
		return SpotExposure{}, false
	}
	e := SpotExposure{Facing: facingLabel(b.Facing), FacingDeg: b.Facing}

	isBlocked, accepted := make([]bool, 360), make([]bool, 360)
	var accept strings.Builder
	for d := 0; d < 360; d++ {
		isBlocked[d] = blocked(float64(d), b)
		x := swellExposure(float64(d), b)
		accepted[d] = x >= acceptedMin
		if d%3 == 0 {
			cmd := "L"
			if d == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&accept, "%s%s ", cmd, dialPoint(float64(d), dialMinRadius+(dialRim-dialMinRadius)*x))
		}
	}
	e.AcceptPath = accept.String() + "Z"

	var land strings.Builder
	for _, run := range circularRuns(isBlocked) {
		// wedge from the center out to the rim across the run's degrees
		from, to := float64(run[0]), float64(run[1]+1)
		large := 0
		if to-from > 180 {
			large = 1
		}
		if to-from >= 360 { // land all round: two half arcs
			fmt.Fprintf(&land, "M%s A%.0f %.0f 0 1 1 %s A%.0f %.0f 0 1 1 %s Z ",
				dialPoint(0, dialRim), dialRim, dialRim, dialPoint(180, dialRim), dialRim, dialRim, dialPoint(0, dialRim))
			continue
		}
		fmt.Fprintf(&land, "M%s L%s A%.0f %.0f 0 %d 1 %s Z ",
			dialPoint(0, 0), dialPoint(from, dialRim), dialRim, dialRim, large, dialPoint(to, dialRim))
	}
	e.LandPath = strings.TrimSpace(land.String())

	var labels []string
	for _, run := range circularRuns(accepted) {
		if run[1]-run[0] >= 359 {
			labels = []string{"all directions"}
			break
		}
		labels = append(labels, fmt.Sprintf("%d–%d°", run[0]%360, run[1]%360))
	}
	e.Accepts = strings.Join(labels, ", ")

	if hasReport {
		e.Primary = exposeSwell(report.PrimaryDegrees, b)
		if report.HasSecondary {
			e.Secondary = exposeSwell(report.SecondaryDegrees, b)
		}
	}
	return e, true
}

func exposeSwell(deg string, b beach) exposedSwell {
	d, err := strconv.ParseFloat(deg, 64)
	if err != nil {
		return exposedSwell{}
	}
	return exposedSwell{Deg: d, Pct: int(math.Round(swellExposure(d, b) * 100)), Has: true}
}
