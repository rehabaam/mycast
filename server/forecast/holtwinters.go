package forecast

import "math"

// hwSeason is the seasonal period in samples — 24 hourly observations = one
// daily cycle.
const hwSeason = 24

// holtWintersForecast produces a `horizon`-step-ahead forecast using additive,
// damped-trend triple exponential smoothing (Holt-Winters / Gardner's method).
//
// Unlike a trained neural net, this has no random initialisation and no
// stochastic training order: given the same input series it always produces
// the same output. The trend is damped (phi < 1) so it decays toward flat
// rather than extrapolating a short-term trend indefinitely — plain
// (undamped) Holt-Winters suffers exactly the unbounded-drift problem this
// project originally used anomaly-based ML to work around.
//
// Smoothing parameters (alpha, beta, gamma) and the damping factor (phi) are
// chosen by grid search, minimizing one-step-ahead SSE over the training
// window.
func holtWintersForecast(values []float64, season, horizon int) []float64 {
	if len(values) < 2*season {
		return flatForecast(values, horizon)
	}

	// The grids are walked by integer index rather than by accumulating a
	// float step, which would drift and skip the upper endpoint (phi = 0.98).
	best := hwFit{sse: math.Inf(1)}
	for ai := 0; ai < 10; ai++ {
		alpha := 0.05 + 0.1*float64(ai)
		for bi := 0; bi < 10; bi++ {
			beta := 0.05 + 0.1*float64(bi)
			for gi := 0; gi < 10; gi++ {
				gamma := 0.05 + 0.1*float64(gi)
				for pi := 0; pi < 8; pi++ {
					phi := 0.7 + 0.04*float64(pi)
					if fit := hwEvaluate(values, season, alpha, beta, gamma, phi); fit.sse < best.sse {
						best = fit
					}
				}
			}
		}
	}
	if best.level == nil {
		// Every fit was NaN/Inf (e.g. non-finite input): no model to
		// extrapolate, so fall back to the plain mean like short series do.
		return flatForecast(values, horizon)
	}

	n := len(values)
	lastLevel := best.level[n-1]
	lastTrend := best.trend[n-1]

	out := make([]float64, horizon)
	dampedSum, phiPow := 0.0, 1.0
	for h := 1; h <= horizon; h++ {
		phiPow *= best.phi
		dampedSum += phiPow
		out[h-1] = lastLevel + dampedSum*lastTrend + best.seasonal[(n+h-1)%season]
	}
	return out
}

// flatForecast repeats the series mean for every step.
func flatForecast(values []float64, horizon int) []float64 {
	mu, _ := meanStd(values)
	out := make([]float64, horizon)
	for i := range out {
		out[i] = mu
	}
	return out
}

func meanStd(data []float64) (mu, sigma float64) {
	if len(data) == 0 {
		return 0, 1
	}
	for _, v := range data {
		mu += v
	}
	mu /= float64(len(data))
	for _, v := range data {
		d := v - mu
		sigma += d * d
	}
	sigma = math.Sqrt(sigma / float64(len(data)))
	if sigma < 1e-8 {
		sigma = 1
	}
	return
}

type hwFit struct {
	sse      float64
	level    []float64
	trend    []float64
	seasonal []float64
	phi      float64
}

// hwEvaluate fits level/trend/seasonal components over the full series with
// the given hyperparameters and reports one-step-ahead SSE.
func hwEvaluate(y []float64, season int, alpha, beta, gamma, phi float64) hwFit {
	n := len(y)
	seasonal := make([]float64, season)
	var firstCycleMean float64
	for i := 0; i < season; i++ {
		firstCycleMean += y[i]
	}
	firstCycleMean /= float64(season)
	for i := 0; i < season; i++ {
		seasonal[i] = y[i] - firstCycleMean
	}

	level := make([]float64, n)
	trend := make([]float64, n)
	level[0] = firstCycleMean
	trend[0] = (y[season] - y[0]) / float64(season)

	var sse float64
	for t := 1; t < n; t++ {
		s := seasonal[t%season]
		prevLevel, prevTrend := level[t-1], trend[t-1]

		level[t] = alpha*(y[t]-s) + (1-alpha)*(prevLevel+phi*prevTrend)
		trend[t] = beta*(level[t]-prevLevel) + (1-beta)*phi*prevTrend
		seasonal[t%season] = gamma*(y[t]-level[t]) + (1-gamma)*s

		fitted := prevLevel + phi*prevTrend + s
		err := y[t] - fitted
		sse += err * err
	}

	return hwFit{sse: sse, level: level, trend: trend, seasonal: seasonal, phi: phi}
}
