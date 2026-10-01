package forecast

import (
	"math"
	"time"
)

// HourlyPoint holds a single hourly forecast value.
type HourlyPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// WindHourly holds direction + speed for one hour.
type WindHourly struct {
	Time         time.Time `json:"time"`
	SpeedKmh     float64   `json:"speed_kmh"`
	DirectionDeg float64   `json:"direction_deg"`
	Cardinal     string    `json:"cardinal"`
}

// PrecipHourly holds precipitation for one hour.
type PrecipHourly struct {
	Time        time.Time `json:"time"`
	AmountMM    float64   `json:"amount_mm"`
	Probability float64   `json:"probability"`
}

// ConditionHourly holds a general weather condition (WMO code + summary) for
// one hour. Only populated on the ECMWF path — the station-only fallback has
// no source for a categorical condition.
type ConditionHourly struct {
	Time    time.Time `json:"time"`
	Code    int       `json:"code"`
	Summary string    `json:"summary"`
}

// ConditionDay summarizes general conditions for a day, e.g. for a mobile
// app's condition icon.
type ConditionDay struct {
	// Summary is the condition at local midday, as a representative
	// single description for the day.
	Summary string            `json:"summary"`
	Hourly  []ConditionHourly `json:"hourly"`
}

// AuroraHourly estimates aurora visibility for one hour, combining NOAA's
// forecast Kp index with local darkness and cloud cover. See aurora.go for
// the (deliberately simple, not scientifically validated) heuristic.
type AuroraHourly struct {
	Time           time.Time `json:"time"`
	Kp             float64   `json:"kp"`
	RequiredKp     float64   `json:"required_kp"` // minimum Kp for visibility at this location
	CloudCoverPct  float64   `json:"cloud_cover_pct"`
	IsDark         bool      `json:"is_dark"`
	ProbabilityPct float64   `json:"probability_pct"`
}

// AuroraDay summarizes aurora visibility chances for a day. Only available
// on the ECMWF path (needs cloud cover and sunrise/sunset, neither of which
// the station-only path has) — nil, and omitted from JSON, on fallback.
type AuroraDay struct {
	MaxProbabilityPct float64        `json:"max_probability_pct"`
	Hourly            []AuroraHourly `json:"hourly"`
}

// DayForecast holds the aggregated and hourly forecast for a single day.
type DayForecast struct {
	Date      string `json:"date"` // YYYY-MM-DD
	DayOfWeek string `json:"day_of_week"`

	// Sunrise/Sunset are only available on the ECMWF path (they're sourced
	// from Open-Meteo's daily block) — nil, and omitted from JSON, on the
	// station-only fallback.
	Sunrise *time.Time `json:"sunrise,omitempty"`
	Sunset  *time.Time `json:"sunset,omitempty"`

	// Aurora is only available on the ECMWF path — see AuroraDay.
	Aurora *AuroraDay `json:"aurora,omitempty"`

	Condition   ConditionDay   `json:"condition"`
	Temperature TemperatureDay `json:"temperature"`
	Humidity    HumidityDay    `json:"humidity"`
	Wind        WindDay        `json:"wind"`
	Precip      PrecipDay      `json:"precipitation"`
}

type TemperatureDay struct {
	MinC   float64       `json:"min_c"`
	MaxC   float64       `json:"max_c"`
	AvgC   float64       `json:"avg_c"`
	Hourly []HourlyPoint `json:"hourly"`

	// Apparent ("feels like") temperature, derived from temp+humidity+wind
	// — see apparent.go.
	ApparentAvgC   float64       `json:"apparent_avg_c"`
	ApparentHourly []HourlyPoint `json:"apparent_hourly"`
}

type HumidityDay struct {
	MinPct float64       `json:"min_pct"`
	MaxPct float64       `json:"max_pct"`
	AvgPct float64       `json:"avg_pct"`
	Hourly []HourlyPoint `json:"hourly"`
}

type WindDay struct {
	AvgSpeedKmh float64      `json:"avg_speed_kmh"`
	MaxSpeedKmh float64      `json:"max_speed_kmh"`
	AvgDirDeg   float64      `json:"avg_direction_deg"`
	Cardinal    string       `json:"cardinal"`
	Hourly      []WindHourly `json:"hourly"`
}

type PrecipDay struct {
	TotalMM     float64        `json:"total_mm"`
	Probability float64        `json:"probability"`
	Hourly      []PrecipHourly `json:"hourly"`
}

// Forecast is the full 3-day forecast response.
type Forecast struct {
	GeneratedAt time.Time `json:"generated_at"`
	StationID   string    `json:"station_id"`
	Model       string    `json:"model"`
	// Stale is true when the forecast should be treated with reduced
	// confidence: the station has delivered no data within the engine's
	// staleness threshold (a stalled scheduler or an offline outdoor module),
	// or there is not enough data to produce any days at all. Recomputing
	// cannot fix this, so it is evaluated against the station's data, not the
	// forecast's own age.
	Stale bool          `json:"stale"`
	Days  []DayForecast `json:"days"`
}

// degreeToCardinal converts a bearing to a compass abbreviation.
func degreeToCardinal(deg float64) string {
	deg = normAngle(deg)
	dirs := []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE",
		"S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}
	idx := int((deg+11.25)/22.5) % 16
	return dirs[idx]
}

// normAngle maps any angle into [0, 360), keeping its fractional part so
// values just inside a compass-sector boundary aren't pushed across it.
func normAngle(deg float64) float64 {
	deg = math.Mod(deg, 360)
	if deg < 0 {
		deg += 360
	}
	return deg
}
