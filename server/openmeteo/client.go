// Package openmeteo fetches NWP forecasts from the free Open-Meteo API for
// a fixed set of coordinates. It supplies the synoptic-scale skill (fronts,
// pressure systems) that a single weather station cannot derive from its own
// history alone.
package openmeteo

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ModelName identifies the upstream NWP model requested from Open-Meteo. It
// is surfaced in API responses so consumers know which model produced a
// given forecast.
const ModelName = "ecmwf_ifs025"

const (
	defaultBaseURL = "https://api.open-meteo.com/v1/forecast"

	// maxResponseBytes caps how much of a response is read, so a broken or
	// hostile upstream can't exhaust memory.
	maxResponseBytes = 4 << 20
)

// Client queries Open-Meteo for a fixed location.
type Client struct {
	http     *http.Client
	baseURL  string
	lat, lon float64
}

// NewClient returns a client for the given coordinates.
func NewClient(lat, lon float64) *Client {
	return &Client{
		http:    &http.Client{Timeout: 10 * time.Second},
		baseURL: defaultBaseURL,
		lat:     lat,
		lon:     lon,
	}
}

// HourlyData holds parallel hourly arrays. Time[i] (Unix seconds, UTC)
// corresponds to index i of every other slice. Daily holds separate,
// day-resolution data (indexed by day, not hour). A value Open-Meteo reported
// as null is NaN here, so callers can tell "missing" from a genuine zero.
type HourlyData struct {
	Time          []int64
	TemperatureC  []float64
	HumidityPct   []float64
	WindSpeedKmh  []float64
	WindDirDeg    []float64
	PrecipMM      []float64
	PrecipProbPct []float64 // 0-100, derived by Open-Meteo; not a raw ECMWF field
	WeatherCode   []float64 // WMO code (table 4677); always a whole number
	CloudCoverPct []float64 // 0-100, total cloud cover
	Daily         DailyData
}

// DailyData holds day-resolution arrays. Time[i] (Unix seconds, UTC
// midnight) corresponds to index i of Sunrise/Sunset.
type DailyData struct {
	Time    []int64
	Sunrise []int64
	Sunset  []int64
}

// Fetch retrieves `pastDays` of recent analysis-quality hourly data (used to
// calibrate a local bias correction) plus `forecastDays` of forecast, in a
// single request.
func (c *Client) Fetch(pastDays, forecastDays int) (*HourlyData, error) {
	params := url.Values{
		"latitude":      {strconv.FormatFloat(c.lat, 'f', 4, 64)},
		"longitude":     {strconv.FormatFloat(c.lon, 'f', 4, 64)},
		"hourly":        {"temperature_2m,relative_humidity_2m,wind_speed_10m,wind_direction_10m,precipitation,precipitation_probability,weather_code,cloud_cover"},
		"daily":         {"sunrise,sunset"},
		"timezone":      {"UTC"},
		"past_days":     {strconv.Itoa(pastDays)},
		"forecast_days": {strconv.Itoa(forecastDays)},
		"models":        {ModelName},
	}
	u := c.baseURL + "?" + params.Encode()

	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("open-meteo request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo: HTTP %d", resp.StatusCode)
	}

	var raw struct {
		Hourly struct {
			Time        []string   `json:"time"`
			Temperature []*float64 `json:"temperature_2m"`
			Humidity    []*float64 `json:"relative_humidity_2m"`
			WindSpeed   []*float64 `json:"wind_speed_10m"`
			WindDir     []*float64 `json:"wind_direction_10m"`
			Precip      []*float64 `json:"precipitation"`
			PrecipProb  []*float64 `json:"precipitation_probability"`
			WeatherCode []*float64 `json:"weather_code"`
			CloudCover  []*float64 `json:"cloud_cover"`
		} `json:"hourly"`
		Daily struct {
			Time    []string `json:"time"`
			Sunrise []string `json:"sunrise"`
			Sunset  []string `json:"sunset"`
		} `json:"daily"`
		Error  bool   `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("open-meteo: parse response: %w", err)
	}
	if raw.Error {
		return nil, fmt.Errorf("open-meteo: %s", raw.Reason)
	}
	if len(raw.Hourly.Time) == 0 {
		return nil, fmt.Errorf("open-meteo: empty hourly response")
	}

	out := &HourlyData{
		Time:          make([]int64, len(raw.Hourly.Time)),
		TemperatureC:  nullsToNaN(raw.Hourly.Temperature),
		HumidityPct:   nullsToNaN(raw.Hourly.Humidity),
		WindSpeedKmh:  nullsToNaN(raw.Hourly.WindSpeed),
		WindDirDeg:    nullsToNaN(raw.Hourly.WindDir),
		PrecipMM:      nullsToNaN(raw.Hourly.Precip),
		PrecipProbPct: nullsToNaN(raw.Hourly.PrecipProb),
		WeatherCode:   nullsToNaN(raw.Hourly.WeatherCode),
		CloudCoverPct: nullsToNaN(raw.Hourly.CloudCover),
	}
	for i, s := range raw.Hourly.Time {
		t, err := time.Parse("2006-01-02T15:04", s)
		if err != nil {
			return nil, fmt.Errorf("open-meteo: parse time %q: %w", s, err)
		}
		out.Time[i] = t.UTC().Unix()
	}

	out.Daily = DailyData{
		Time:    make([]int64, len(raw.Daily.Time)),
		Sunrise: make([]int64, len(raw.Daily.Sunrise)),
		Sunset:  make([]int64, len(raw.Daily.Sunset)),
	}
	for i, s := range raw.Daily.Time {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, fmt.Errorf("open-meteo: parse daily date %q: %w", s, err)
		}
		out.Daily.Time[i] = t.UTC().Unix()
	}
	for i, s := range raw.Daily.Sunrise {
		t, err := time.Parse("2006-01-02T15:04", s)
		if err != nil {
			return nil, fmt.Errorf("open-meteo: parse sunrise %q: %w", s, err)
		}
		out.Daily.Sunrise[i] = t.UTC().Unix()
	}
	for i, s := range raw.Daily.Sunset {
		t, err := time.Parse("2006-01-02T15:04", s)
		if err != nil {
			return nil, fmt.Errorf("open-meteo: parse sunset %q: %w", s, err)
		}
		out.Daily.Sunset[i] = t.UTC().Unix()
	}

	return out, nil
}

// nullsToNaN converts decoded JSON numbers to plain floats, mapping null to
// NaN. Decoding straight into []float64 would silently turn null into 0.
func nullsToNaN(in []*float64) []float64 {
	out := make([]float64, len(in))
	for i, p := range in {
		if p == nil {
			out[i] = math.NaN()
		} else {
			out[i] = *p
		}
	}
	return out
}
