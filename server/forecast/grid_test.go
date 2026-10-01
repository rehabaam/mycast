package forecast

import (
	"testing"

	"github.com/rehabaam/mycast/netatmo"
)

func obsAt(hour int64, temp float64) netatmo.Observation {
	return netatmo.Observation{
		Timestamp:   hour * secondsPerHour,
		Temperature: temp,
		Humidity:    50 + temp,
		WindSpeed:   temp / 2,
		WindAngle:   350,
		Rain:        1,
	}
}

func TestHourlyGridIsContiguousAndFillsGaps(t *testing.T) {
	// Hours 100, 101, then a 3-hour gap, then 105.
	in := []netatmo.Observation{obsAt(100, 10), obsAt(101, 12), obsAt(105, 20)}

	grid := hourlyGrid(in)

	if len(grid) != 6 {
		t.Fatalf("len(grid) = %d, want 6 (hours 100-105)", len(grid))
	}
	for i, o := range grid {
		if want := int64(100+i) * secondsPerHour; o.Timestamp != want {
			t.Errorf("grid[%d].Timestamp = %d, want %d", i, o.Timestamp, want)
		}
	}
	// Real samples are untouched.
	if grid[1].Temperature != 12 || grid[5].Temperature != 20 {
		t.Errorf("real samples altered: %v, %v", grid[1], grid[5])
	}
	// Gap hours are interpolated between 12 (hour 101) and 20 (hour 105).
	for i, want := range []float64{14, 16, 18} {
		if got := grid[2+i].Temperature; got != want {
			t.Errorf("gap hour %d temperature = %v, want %v", 102+i, got, want)
		}
	}
	gap := grid[3]
	if gap.Humidity != 66 || gap.WindSpeed != 8 {
		t.Errorf("gap humidity/wind = %v/%v, want 66/8 (interpolated)", gap.Humidity, gap.WindSpeed)
	}
	// Directions carry forward instead of interpolating across the wrap, and
	// rain is not invented.
	if gap.WindAngle != 350 {
		t.Errorf("gap WindAngle = %v, want 350 (carried forward)", gap.WindAngle)
	}
	if gap.Rain != 0 {
		t.Errorf("gap Rain = %v, want 0", gap.Rain)
	}
}

func TestHourlyGridPassesThroughAContiguousSeries(t *testing.T) {
	in := []netatmo.Observation{obsAt(10, 1), obsAt(11, 2), obsAt(12, 3)}
	grid := hourlyGrid(in)
	if len(grid) != 3 {
		t.Fatalf("len(grid) = %d, want 3", len(grid))
	}
	for i := range in {
		if grid[i] != in[i] {
			t.Errorf("grid[%d] = %+v, want %+v", i, grid[i], in[i])
		}
	}
}

func TestHourlyGridEmpty(t *testing.T) {
	if got := hourlyGrid(nil); got != nil {
		t.Errorf("hourlyGrid(nil) = %v, want nil", got)
	}
}
