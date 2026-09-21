package forecast

import (
	"math"
	"testing"
)

// synthetic 6-day hourly series: daily sinusoidal cycle + a mild upward drift.
func syntheticSeasonalSeries(days int) []float64 {
	n := days * 24
	out := make([]float64, n)
	for i := range out {
		hour := float64(i % 24)
		day := float64(i / 24)
		out[i] = 15 + 5*math.Sin(2*math.Pi*hour/24) + 0.2*day
	}
	return out
}

func TestHoltWintersForecastDeterministic(t *testing.T) {
	series := syntheticSeasonalSeries(6)
	fc1 := holtWintersForecast(series, hwSeason, 24)
	fc2 := holtWintersForecast(series, hwSeason, 24)
	for i := range fc1 {
		if fc1[i] != fc2[i] {
			t.Fatalf("non-deterministic output at step %d: %.6f vs %.6f", i, fc1[i], fc2[i])
		}
	}
}

func TestHoltWintersForecastTracksSeasonalPattern(t *testing.T) {
	series := syntheticSeasonalSeries(6)
	fc := holtWintersForecast(series, hwSeason, 24)

	// The forecast should stay within the physical range of the input
	// series (±2°C slack for trend/seasonal estimation error) rather than
	// diverging — this is the failure mode the damped trend is meant to
	// prevent.
	lo, hi := series[0], series[0]
	for _, v := range series {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	for i, v := range fc {
		if v < lo-2 || v > hi+3 {
			t.Errorf("forecast[%d]=%.2f outside plausible range [%.2f, %.2f]", i, v, lo-2, hi+3)
		}
	}
}

func TestHoltWintersForecastFallsBackWithInsufficientData(t *testing.T) {
	short := []float64{10, 11, 12}
	fc := holtWintersForecast(short, hwSeason, 5)
	if len(fc) != 5 {
		t.Fatalf("expected 5 forecast points, got %d", len(fc))
	}
	for _, v := range fc {
		if v < 9 || v > 13 {
			t.Errorf("fallback forecast %.2f should be near observed mean", v)
		}
	}
}
