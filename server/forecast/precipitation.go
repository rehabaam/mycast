package forecast

import "math"

// precipResult holds per-hour precipitation amount and probability forecasts.
type precipResult struct {
	amounts       []float64
	probabilities []float64
}

// precipForecast uses a persistence + climatological pattern model.
// Rain is kept separate from the ML models because it is intermittent and
// highly non-Gaussian — regression models tend to predict near-zero always.
func precipForecast(rains []float64, steps int) precipResult {
	n := len(rains)
	res := precipResult{
		amounts:       make([]float64, steps),
		probabilities: make([]float64, steps),
	}
	if n == 0 {
		return res
	}

	const wetThreshold = 0.1 // mm/h

	// Climatological statistics bucketed by hour-of-day.
	hourCount := make([]int, 24)
	hourWet := make([]int, 24)
	hourAmount := make([]float64, 24)
	for i, r := range rains {
		h := i % 24
		hourCount[h]++
		if r > wetThreshold {
			hourWet[h]++
			hourAmount[h] += r
		}
	}

	hourProb := make([]float64, 24)
	hourMean := make([]float64, 24)
	for h := 0; h < 24; h++ {
		if hourCount[h] > 0 {
			hourProb[h] = float64(hourWet[h]) / float64(hourCount[h])
			if hourWet[h] > 0 {
				hourMean[h] = hourAmount[h] / float64(hourWet[h])
			}
		}
	}

	// Recent rain rate via exponential smoothing.
	recentRate := expSmooth(rains, 0.4)
	lastHour := n % 24

	for h := 1; h <= steps; h++ {
		hour := (lastHour + h) % 24

		normalised := recentRate / math.Max(recentRate, 1)
		blendedProb := 0.5*clamp(normalised, 0, 1) + 0.5*hourProb[hour]
		blendedAmt := 0.5*recentRate + 0.5*hourMean[hour]

		// Forecast skill decays with horizon.
		damping := math.Exp(-0.02 * float64(h))
		prob := damping*blendedProb + (1-damping)*0.3 // 0.3 = climatological base rate
		amt := damping * blendedAmt

		res.probabilities[h-1] = clamp(prob, 0, 1)
		res.amounts[h-1] = math.Max(0, amt)
	}

	return res
}

func expSmooth(data []float64, alpha float64) float64 {
	if len(data) == 0 {
		return 0
	}
	v := data[0]
	for _, d := range data[1:] {
		v = alpha*d + (1-alpha)*v
	}
	return v
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
