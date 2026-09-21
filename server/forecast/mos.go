package forecast

// hourlyBias is an additive correction per hour-of-day (UTC), learned by
// comparing an NWP model's recent hindcast against actual station
// observations at the same timestamps — a standard Model Output Statistics
// (MOS) approach. The NWP model supplies synoptic-scale skill (fronts,
// pressure systems) a single station cannot see; this correction captures
// the station's own persistent offset from the model's grid point (e.g. a
// sheltered garden reading warmer than the surrounding open terrain).
type hourlyBias [24]float64

// minBiasSamples is the minimum number of matched observations required
// before an hourly bucket's bias is trusted; buckets with fewer samples are
// left uncorrected (zero) rather than extrapolating from too little evidence.
const minBiasSamples = 2

// fitHourlyBias computes mean(actual-predicted) bucketed by UTC hour-of-day,
// over timestamps present in both maps.
func fitHourlyBias(actualByTS, predictedByTS map[int64]float64) hourlyBias {
	var sum, count [24]float64
	for ts, actual := range actualByTS {
		pred, ok := predictedByTS[ts]
		if !ok {
			continue
		}
		h := hourOfDay(ts)
		sum[h] += actual - pred
		count[h]++
	}

	var bias hourlyBias
	for h := 0; h < 24; h++ {
		if count[h] >= minBiasSamples {
			bias[h] = sum[h] / count[h]
		}
	}
	return bias
}

// apply adds the hour-matched correction to each forecast value.
func (b hourlyBias) apply(forecastTS []int64, values []float64) []float64 {
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = v + b[hourOfDay(forecastTS[i])]
	}
	return out
}

// hourOfDay extracts the UTC hour-of-day [0,23] from a Unix timestamp.
// Integer division works directly because the Unix epoch itself starts at
// UTC midnight.
func hourOfDay(ts int64) int {
	return int((ts / 3600) % 24)
}
