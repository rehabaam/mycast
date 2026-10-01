package forecast

import "testing"

func TestPrecipForecastClimatologyIsAlignedToHourOfDay(t *testing.T) {
	// A week of hourly samples that ends at 10:00 UTC, with rain only at
	// 15:00 every day.
	const days = 7
	const lastHour = 10
	n := days * 24
	rains := make([]float64, n)
	for i := range rains {
		hourOfDay := ((lastHour-(n-1-i))%24 + 24) % 24
		if hourOfDay == 15 {
			rains[i] = 5
		}
	}

	res := precipForecast(rains, lastHour, 24)

	// Forecast step h is the hour h after the final sample, so 15:00 is step
	// 5, i.e. index 4.
	peak := 0
	for i, p := range res.probabilities {
		if p > res.probabilities[peak] {
			peak = i
		}
	}
	if peak != 4 {
		t.Errorf("rain probability peaks at step index %d, want 4 (15:00 UTC)", peak)
	}
}

func TestPrecipForecastIsIndependentOfWhereTheSeriesStarts(t *testing.T) {
	// The same wall-clock pattern, delivered as series of different lengths,
	// must give the same answer: hour-of-day comes from lastHour, not from
	// the slice index.
	build := func(n int) []float64 {
		const lastHour = 3
		rains := make([]float64, n)
		for i := range rains {
			if ((lastHour-(n-1-i))%24+24)%24 == 8 {
				rains[i] = 2
			}
		}
		return rains
	}
	a := precipForecast(build(7*24), 3, 24)
	b := precipForecast(build(7*24+5), 3, 24)
	for i := range a.probabilities {
		if d := a.probabilities[i] - b.probabilities[i]; d > 1e-9 || d < -1e-9 {
			t.Fatalf("step %d differs: %v vs %v", i, a.probabilities[i], b.probabilities[i])
		}
	}
}

func TestPrecipForecastNoDataIsZero(t *testing.T) {
	res := precipForecast(nil, 0, 5)
	if len(res.amounts) != 5 || len(res.probabilities) != 5 {
		t.Fatalf("lengths = %d/%d, want 5/5", len(res.amounts), len(res.probabilities))
	}
	for i := range res.amounts {
		if res.amounts[i] != 0 || res.probabilities[i] != 0 {
			t.Errorf("step %d = %v/%v, want 0/0", i, res.amounts[i], res.probabilities[i])
		}
	}
}
