package forecast

import "testing"

func TestFitHourlyBiasLearnsConsistentOffset(t *testing.T) {
	actual := map[int64]float64{}
	predicted := map[int64]float64{}

	// Station consistently reads 1.5°C warmer than the model at hour 12,
	// across several days, and matches exactly at hour 0.
	for day := int64(0); day < 5; day++ {
		noon := day*86400 + 12*3600
		midnight := day * 86400
		actual[noon] = 20.0
		predicted[noon] = 18.5

		actual[midnight] = 10.0
		predicted[midnight] = 10.0
	}

	bias := fitHourlyBias(actual, predicted)
	if got := bias[12]; got < 1.4 || got > 1.6 {
		t.Errorf("bias[12] = %.2f, want ~1.5", got)
	}
	if got := bias[0]; got < -0.01 || got > 0.01 {
		t.Errorf("bias[0] = %.2f, want ~0", got)
	}
	// Untouched hours should stay at zero rather than extrapolating.
	if got := bias[6]; got != 0 {
		t.Errorf("bias[6] = %.2f, want 0 (no samples)", got)
	}
}

func TestFitHourlyBiasIgnoresSparseBuckets(t *testing.T) {
	actual := map[int64]float64{3600 * 5: 100.0} // one wild outlier sample at hour 5
	predicted := map[int64]float64{3600 * 5: 0.0}

	bias := fitHourlyBias(actual, predicted)
	if bias[5] != 0 {
		t.Errorf("bias[5] = %.2f, want 0 (below minBiasSamples threshold)", bias[5])
	}
}

func TestHourlyBiasApply(t *testing.T) {
	var bias hourlyBias
	bias[3] = 2.0

	ts := []int64{3 * 3600, 4 * 3600}
	values := []float64{10.0, 10.0}

	out := bias.apply(ts, values)
	if out[0] != 12.0 {
		t.Errorf("out[0] = %.2f, want 12.0 (bias applied)", out[0])
	}
	if out[1] != 10.0 {
		t.Errorf("out[1] = %.2f, want 10.0 (no bias at hour 4)", out[1])
	}
}

func TestHourOfDay(t *testing.T) {
	cases := map[int64]int{
		0:            0,
		3600:         1,
		86399:        23,
		86400:        0,
		86400 + 3661: 1,
	}
	for ts, want := range cases {
		if got := hourOfDay(ts); got != want {
			t.Errorf("hourOfDay(%d) = %d, want %d", ts, got, want)
		}
	}
}
