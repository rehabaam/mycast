package forecast

import "math"

// apparentTemperatureC estimates "feels like" temperature from air
// temperature, relative humidity, and wind speed, using the standard
// Australian Bureau of Meteorology formula. Computed locally (rather than
// taken from Open-Meteo's own apparent_temperature field) so it's available
// identically on both the ECMWF and station-only fallback paths, from
// whatever temperature/humidity/wind-speed forecast each path already
// produces.
func apparentTemperatureC(tempC, humidityPct, windSpeedKmh float64) float64 {
	windMS := windSpeedKmh / 3.6
	vaporPressure := (humidityPct / 100.0) * 6.105 * math.Exp(17.27*tempC/(237.7+tempC))
	return tempC + 0.33*vaporPressure - 0.70*windMS - 4.00
}
