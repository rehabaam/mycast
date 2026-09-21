// Package noaa fetches geomagnetic activity (Kp index) forecasts from NOAA's
// Space Weather Prediction Center — the input needed to estimate aurora
// visibility, which has nothing to do with atmospheric weather models and so
// isn't available from Open-Meteo/ECMWF.
package noaa

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const kpForecastURL = "https://services.swpc.noaa.gov/products/noaa-planetary-k-index-forecast.json"

// Client queries NOAA SWPC. Unlike the Netatmo/Open-Meteo clients, it takes
// no location — the Kp index is a single global geomagnetic activity value,
// not location-specific.
type Client struct {
	http *http.Client
}

// NewClient returns a client for NOAA SWPC's public, unauthenticated API.
func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 10 * time.Second}}
}

// KpPoint is one 3-hour Kp index bucket.
type KpPoint struct {
	Time int64 // Unix seconds, UTC — start of the 3-hour bucket
	Kp   float64
}

// FetchKpForecast returns NOAA's rolling ~10-day window of observed,
// estimated, and predicted Kp values (3-hour resolution). Callers should
// filter to the time range they need — this returns everything NOAA
// provides, past and future.
func (c *Client) FetchKpForecast() ([]KpPoint, error) {
	resp, err := c.http.Get(kpForecastURL)
	if err != nil {
		return nil, fmt.Errorf("noaa kp forecast request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("noaa kp forecast: HTTP %d", resp.StatusCode)
	}

	var raw []struct {
		TimeTag string  `json:"time_tag"`
		Kp      float64 `json:"kp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("noaa kp forecast: parse response: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("noaa kp forecast: empty response")
	}

	out := make([]KpPoint, len(raw))
	for i, r := range raw {
		t, err := time.Parse("2006-01-02T15:04:05", r.TimeTag)
		if err != nil {
			return nil, fmt.Errorf("noaa kp forecast: parse time %q: %w", r.TimeTag, err)
		}
		out[i] = KpPoint{Time: t.UTC().Unix(), Kp: r.Kp}
	}
	return out, nil
}
