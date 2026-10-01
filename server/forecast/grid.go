package forecast

import "github.com/rehabaam/mycast/netatmo"

const secondsPerHour = 3600

// hourlyGrid turns the store's observations into a contiguous hourly series
// from the first to the last stored hour, so that slice position maps
// one-to-one onto clock hours. The seasonal models index by position, and
// that is only meaningful when no hour is missing.
//
// obs must be ascending with unique, hour-aligned timestamps, as the store
// guarantees. Missing hours are filled: temperature, humidity and speeds by
// linear interpolation, directions by carrying the previous value forward
// (interpolating across the 0/360 wrap would sweep through the wrong half of
// the compass), and rain as zero (it is an accumulation, not a state).
func hourlyGrid(obs []netatmo.Observation) []netatmo.Observation {
	if len(obs) == 0 {
		return nil
	}
	first, last := obs[0].Timestamp, obs[len(obs)-1].Timestamp
	out := make([]netatmo.Observation, 0, (last-first)/secondsPerHour+1)

	next := 0
	for ts := first; ts <= last; ts += secondsPerHour {
		for next < len(obs) && obs[next].Timestamp < ts {
			next++ // tolerate a stray unaligned sample rather than mis-index
		}
		if next >= len(obs) {
			break
		}
		if obs[next].Timestamp == ts {
			out = append(out, obs[next])
			next++
			continue
		}
		prev, after := out[len(out)-1], obs[next]
		frac := float64(ts-prev.Timestamp) / float64(after.Timestamp-prev.Timestamp)
		out = append(out, netatmo.Observation{
			Timestamp:   ts,
			Temperature: lerp(prev.Temperature, after.Temperature, frac),
			Humidity:    lerp(prev.Humidity, after.Humidity, frac),
			WindSpeed:   lerp(prev.WindSpeed, after.WindSpeed, frac),
			GustSpeed:   lerp(prev.GustSpeed, after.GustSpeed, frac),
			WindAngle:   prev.WindAngle,
			GustAngle:   prev.GustAngle,
		})
	}
	return out
}

func lerp(a, b, frac float64) float64 {
	return a + (b-a)*frac
}
