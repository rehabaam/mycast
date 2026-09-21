package forecast

import "math"

// geomagPoleLatDeg/LonDeg approximate the current geomagnetic (dipole) north
// pole location — distinct from the geographic pole, and the reason places
// like Finland see aurora at latitudes where a naive geographic-latitude
// model would say they shouldn't. Drifts slowly (~0.1-0.2°/year); accurate
// enough for aurora visibility estimation without needing a full IGRF model.
const (
	geomagPoleLatDeg = 80.7
	geomagPoleLonDeg = -72.7
)

// geomagneticLatitude approximates geomagnetic latitude from geographic
// coordinates using the standard dipole approximation.
func geomagneticLatitude(lat, lon float64) float64 {
	latR := lat * math.Pi / 180
	lonR := lon * math.Pi / 180
	poleLatR := geomagPoleLatDeg * math.Pi / 180
	poleLonR := geomagPoleLonDeg * math.Pi / 180

	sinGM := math.Sin(latR)*math.Sin(poleLatR) +
		math.Cos(latR)*math.Cos(poleLatR)*math.Cos(lonR-poleLonR)
	return math.Asin(clampUnit(sinGM)) * 180 / math.Pi
}

func clampUnit(v float64) float64 {
	if v < -1 {
		return -1
	}
	if v > 1 {
		return 1
	}
	return v
}

// kpOvalBoundary maps Kp index to the auroral oval's equatorward boundary,
// in geomagnetic latitude degrees — i.e. at that Kp, the oval (and aurora
// visibility) extends down to that latitude. Standard reference table used
// by aurora-forecasting tools.
var kpOvalBoundary = [10]float64{
	66.5, 64.5, 62.4, 60.4, 58.3, 56.3, 54.2, 52.2, 50.1, 48.1,
}

// requiredKp returns the minimum Kp index needed for the auroral oval to
// reach the given geomagnetic latitude, by inverting kpOvalBoundary via
// linear interpolation.
func requiredKp(geomagLat float64) float64 {
	if geomagLat >= kpOvalBoundary[0] {
		return 0
	}
	if geomagLat <= kpOvalBoundary[9] {
		return 9
	}
	for kp := 0; kp < 9; kp++ {
		hi, lo := kpOvalBoundary[kp], kpOvalBoundary[kp+1]
		if geomagLat <= hi && geomagLat >= lo {
			frac := (hi - geomagLat) / (hi - lo)
			return float64(kp) + frac
		}
	}
	return 9
}

// auroraProbabilityPct is a deliberately simple heuristic, not a validated
// aurora nowcast model: it scales from 0% at the visibility threshold to
// 100% three Kp steps above it, zeroes out in daylight, and derates by
// cloud cover (aurora is invisible through overcast skies regardless of
// geomagnetic activity). Real aurora prediction also depends on solar wind
// speed and IMF Bz orientation, which aren't modeled here — this answers
// "is it geomagnetically active enough, and will I have clear dark sky,"
// not "is a substorm about to happen."
func auroraProbabilityPct(kp, requiredKp, cloudCoverPct float64, isDark bool) float64 {
	if !isDark {
		return 0
	}
	margin := kp - requiredKp
	if margin < 0 {
		return 0
	}
	base := clamp(margin/3.0, 0, 1) * 100
	return base * (1 - clamp(cloudCoverPct/100.0, 0, 1))
}
