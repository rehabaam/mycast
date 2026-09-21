package forecast

// wmoCondition maps a WMO weather interpretation code (table 4677, as used
// by Open-Meteo) to a short human-readable summary.
var wmoCondition = map[int]string{
	0: "Clear sky", 1: "Mainly clear", 2: "Partly cloudy", 3: "Overcast",
	45: "Fog", 48: "Depositing rime fog",
	51: "Light drizzle", 53: "Moderate drizzle", 55: "Dense drizzle",
	56: "Light freezing drizzle", 57: "Dense freezing drizzle",
	61: "Slight rain", 63: "Moderate rain", 65: "Heavy rain",
	66: "Light freezing rain", 67: "Heavy freezing rain",
	71: "Slight snow fall", 73: "Moderate snow fall", 75: "Heavy snow fall",
	77: "Snow grains",
	80: "Slight rain showers", 81: "Moderate rain showers", 82: "Violent rain showers",
	85: "Slight snow showers", 86: "Heavy snow showers",
	95: "Thunderstorm", 96: "Thunderstorm with slight hail", 99: "Thunderstorm with heavy hail",
}

// conditionSummary returns a short human-readable description for a WMO
// weather code, or "Unknown" for an unrecognized or absent code (e.g. on the
// station-only fallback path, which has no weather-code source).
func conditionSummary(code int) string {
	if s, ok := wmoCondition[code]; ok {
		return s
	}
	return "Unknown"
}
