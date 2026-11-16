package main

// Nearshore calibration of the classifier's wave trains. The global model's
// partitions carry each swell's period and direction, but near the coast
// its coarse grid misjudges how much energy arrives (sheltering, refraction
// and shoaling inside bays happen below its resolution). Where a spot lies
// inside an NWPS domain, the nearshore model's sea state is the better
// measure of size, so each hour's partitions are rescaled to match it:
// their shape stays the global model's, their energy becomes NWPS's.

import "math"

// nearshoreFadeHours is how far, in hours, an NWPS calibration reaches into
// hours that have none (backfilled hours before the run, and past the 144 h
// NWPS horizon), fading linearly back to the raw global trains. Without the
// fade, surf heights would step at the horizon.
const nearshoreFadeHours = 24

// nearshoreFactors is how one hour's trains are resized to NWPS: swell
// multiplies the swell partitions' heights, wind the wind sea's. With an
// NWPS swell height, swell partitions take its energy and the wind sea the
// rest of the total; without one (the field can be missing), every train
// scales together to the total. ok is false when there is nothing to scale
// or nothing to scale it to.
func nearshoreFactors(comps []SwellComponent, n *nwpsPoint) (swell, wind float64, ok bool) {
	if n == nil || n.H <= 0 {
		return 1, 1, false
	}
	swellE, windE := 0.0, 0.0
	for _, c := range comps {
		if c.WindSea {
			windE += c.HeightM * c.HeightM
		} else {
			swellE += c.HeightM * c.HeightM
		}
	}
	if swellE+windE == 0 {
		return 1, 1, false
	}
	if n.S > 0 && swellE > 0 {
		swell, wind = n.S/math.Sqrt(swellE), 1
		if windE > 0 {
			wind = math.Sqrt(math.Max(0, n.H*n.H-n.S*n.S)) / math.Sqrt(windE)
		}
		return swell, wind, true
	}
	k := n.H / math.Sqrt(swellE+windE)
	return k, k, true
}

// calibrateNearshore rescales each hour's trains to the hour's NWPS sample
// (ns, parallel to hours; nil where there is none). Hours without one
// borrow the nearest calibrated hour's factors, faded toward 1 over
// nearshoreFadeHours. Hours are consecutive and hourly (buildSpotForecast).
func calibrateNearshore(hours []hourSwells, ns []*nwpsPoint) {
	type factors struct {
		swell, wind float64
		ok          bool
	}
	fs := make([]factors, len(hours))
	calibrated := false
	for i := range hours {
		s, w, ok := nearshoreFactors(hours[i].Comps, ns[i])
		fs[i] = factors{s, w, ok}
		calibrated = calibrated || ok
	}
	if !calibrated {
		return
	}
	for i := range hours {
		s, w := fs[i].swell, fs[i].wind
		if !fs[i].ok {
			s, w = 1, 1
			for d := 1; d < nearshoreFadeHours; d++ {
				j := -1
				if i-d >= 0 && fs[i-d].ok {
					j = i - d
				} else if i+d < len(fs) && fs[i+d].ok {
					j = i + d
				}
				if j >= 0 {
					keep := 1 - float64(d)/nearshoreFadeHours
					s = 1 + (fs[j].swell-1)*keep
					w = 1 + (fs[j].wind-1)*keep
					break
				}
			}
		}
		if s == 1 && w == 1 {
			continue
		}
		comps := make([]SwellComponent, len(hours[i].Comps))
		for k, c := range hours[i].Comps {
			if c.WindSea {
				c.HeightM *= w
			} else {
				c.HeightM *= s
			}
			comps[k] = c
		}
		hours[i].Comps = comps
	}
}
