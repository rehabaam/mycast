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

// An additive model must be translation-equivariant: shifting every input by
// a constant shifts every forecast by exactly that constant. Unlike a
// determinism check, this fails if the level, seasonal or parameter-search
// logic quietly depends on absolute values.
func TestHoltWintersForecastIsTranslationEquivariant(t *testing.T) {
	series := syntheticSeasonalSeries(6)
	shifted := make([]float64, len(series))
	for i, v := range series {
		shifted[i] = v + 7.5
	}

	base := holtWintersForecast(series, hwSeason, 48)
	moved := holtWintersForecast(shifted, hwSeason, 48)

	for i := range base {
		if d := moved[i] - (base[i] + 7.5); d > 1e-6 || d < -1e-6 {
			t.Fatalf("step %d: shifted forecast %.6f, want %.6f", i, moved[i], base[i]+7.5)
		}
	}
}

// The trend is damped so that a recent fall doesn't get extrapolated
// indefinitely — the failure FORECAST.md describes. Five steady days followed
// by a sharp fall (0.3 °C/h over the last day) leaves a strongly negative
// trend at the end; continuing it linearly would put the last day of the
// horizon near -11 °C, and damping must hold it well above that.
func TestHoltWintersDampsAStrongTrend(t *testing.T) {
	const hours, fallHours, fallPerHour = 6 * 24, 24, 0.3
	series := make([]float64, hours)
	for i := range series {
		series[i] = 15 + 4*math.Sin(2*math.Pi*float64(i%24)/24)
		if k := i - (hours - fallHours); k > 0 {
			series[i] -= fallPerHour * float64(k)
		}
	}

	fc := holtWintersForecast(series, hwSeason, 72)

	var gotTail, linearTail float64
	end := series[hours-1]
	for h := 49; h <= 72; h++ {
		gotTail += fc[h-1] / 24
		linearTail += (end - fallPerHour*float64(h)) / 24
	}
	if gotTail < linearTail+4 {
		t.Errorf("last-day mean %.2f is within 4 °C of undamped extrapolation %.2f: trend is not being damped", gotTail, linearTail)
	}
}

func TestHoltWintersDoesNotPanicOnNonFiniteInput(t *testing.T) {
	series := syntheticSeasonalSeries(3)
	series[10] = math.NaN()
	fc := holtWintersForecast(series, hwSeason, 12)
	if len(fc) != 12 {
		t.Fatalf("len(fc) = %d, want 12", len(fc))
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
