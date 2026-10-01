package api

import (
	"math"
	"time"

	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
)

// currentResponse is the /current wire format: snake_case keys with units in
// the name and RFC 3339 timestamps, matching /forecast. It is deliberately
// separate from netatmo.Current, so upstream-facing types can change without
// breaking API consumers.
type currentResponse struct {
	// Timestamp is when the station measured the reading; FetchedAt is when
	// this service last retrieved it from Netatmo. Stale is true when
	// FetchedAt is older than the staleness threshold, meaning refreshes are
	// failing and everything below is the last good reading, not a current one.
	Timestamp time.Time `json:"timestamp"`
	FetchedAt time.Time `json:"fetched_at"`
	Stale     bool      `json:"stale"`

	// OutdoorAvailable is false when the outdoor module is missing or
	// unreachable; the outdoor_* and apparent_temp_c values are then zero and
	// should not be shown as readings.
	OutdoorAvailable   bool    `json:"outdoor_available"`
	OutdoorTempC       float64 `json:"outdoor_temp_c"`
	OutdoorHumidityPct float64 `json:"outdoor_humidity_pct"`
	ApparentTempC      float64 `json:"apparent_temp_c"` // "feels like"

	IndoorTempC       float64 `json:"indoor_temp_c"`
	IndoorHumidityPct float64 `json:"indoor_humidity_pct"`

	PressureHpa   float64 `json:"pressure_hpa"`
	PressureTrend string  `json:"pressure_trend"` // "up", "down", "stable" — computed by Netatmo
	TempTrend     string  `json:"temp_trend"`

	WindSpeedKmh float64 `json:"wind_speed_kmh"`
	WindAngleDeg float64 `json:"wind_angle_deg"`
	GustSpeedKmh float64 `json:"gust_speed_kmh"`
	GustAngleDeg float64 `json:"gust_angle_deg"`
	RainMM       float64 `json:"rain_mm"`
	SumRain1hMM  float64 `json:"sum_rain_1h_mm"`
	SumRain24hMM float64 `json:"sum_rain_24h_mm"`

	// Actual recorded outdoor extremes for today, as tracked by the station.
	TodayOutdoorMinC  float64    `json:"today_outdoor_min_c"`
	TodayOutdoorMaxC  float64    `json:"today_outdoor_max_c"`
	TodayOutdoorMinAt *time.Time `json:"today_outdoor_min_at,omitempty"`
	TodayOutdoorMaxAt *time.Time `json:"today_outdoor_max_at,omitempty"`

	Modules []moduleResponse `json:"modules"`
}

type moduleResponse struct {
	Type           string     `json:"type"`
	Name           string     `json:"name"`
	BatteryPercent int        `json:"battery_percent"`
	Reachable      bool       `json:"reachable"`
	RFStatus       int        `json:"rf_status"`
	LastSeen       *time.Time `json:"last_seen,omitempty"`
}

func newCurrentResponse(cur *netatmo.Current, stale bool) currentResponse {
	resp := currentResponse{
		Timestamp:          unixTime(cur.Timestamp),
		FetchedAt:          cur.FetchedAt.UTC().Truncate(time.Second),
		Stale:              stale,
		OutdoorAvailable:   cur.OutdoorAvailable,
		OutdoorTempC:       cur.OutdoorTemp,
		OutdoorHumidityPct: cur.OutdoorHumidity,
		IndoorTempC:        cur.IndoorTemp,
		IndoorHumidityPct:  cur.IndoorHumidity,
		PressureHpa:        cur.Pressure,
		PressureTrend:      cur.PressureTrend,
		TempTrend:          cur.TempTrend,
		WindSpeedKmh:       cur.WindSpeed,
		WindAngleDeg:       cur.WindAngle,
		GustSpeedKmh:       cur.GustSpeed,
		GustAngleDeg:       cur.GustAngle,
		RainMM:             cur.Rain,
		SumRain1hMM:        cur.SumRain1h,
		SumRain24hMM:       cur.SumRain24h,
		TodayOutdoorMinC:   cur.TodayOutdoorMinC,
		TodayOutdoorMaxC:   cur.TodayOutdoorMaxC,
		TodayOutdoorMinAt:  optionalUnixTime(cur.TodayOutdoorMinAt),
		TodayOutdoorMaxAt:  optionalUnixTime(cur.TodayOutdoorMaxAt),
		Modules:            make([]moduleResponse, 0, len(cur.Modules)),
	}
	if cur.OutdoorAvailable {
		resp.ApparentTempC = math.Round(forecast.ApparentTemperatureC(cur.OutdoorTemp, cur.OutdoorHumidity, cur.WindSpeed)*100) / 100
	}
	for _, m := range cur.Modules {
		resp.Modules = append(resp.Modules, moduleResponse{
			Type:           m.Type,
			Name:           m.Name,
			BatteryPercent: m.BatteryPercent,
			Reachable:      m.Reachable,
			RFStatus:       m.RFStatus,
			LastSeen:       optionalUnixTime(m.LastSeen),
		})
	}
	return resp
}

func unixTime(sec int64) time.Time {
	return time.Unix(sec, 0).UTC()
}

// optionalUnixTime returns nil for the zero value, which Netatmo uses for
// "not reported".
func optionalUnixTime(sec int64) *time.Time {
	if sec == 0 {
		return nil
	}
	t := unixTime(sec)
	return &t
}
