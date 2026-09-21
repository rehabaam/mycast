package forecast

import "math"

// circularMean computes the mean bearing (degrees) of a slice of angles,
// correctly handling the 0/360 wraparound via unit-vector decomposition.
func circularMean(angles []float64) float64 {
	if len(angles) == 0 {
		return 0
	}
	var sumSin, sumCos float64
	for _, a := range angles {
		rad := a * math.Pi / 180.0
		sumSin += math.Sin(rad)
		sumCos += math.Cos(rad)
	}
	m := math.Atan2(sumSin/float64(len(angles)), sumCos/float64(len(angles))) * 180.0 / math.Pi
	if m < 0 {
		m += 360
	}
	return m
}
