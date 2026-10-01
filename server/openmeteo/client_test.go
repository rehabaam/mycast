package openmeteo

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const fixture = `{
  "hourly": {
    "time": ["2026-06-15T10:00", "2026-06-15T11:00", "2026-06-15T12:00"],
    "temperature_2m": [10.5, null, 12.0],
    "relative_humidity_2m": [80, 81, 82],
    "wind_speed_10m": [5, 6, 7],
    "wind_direction_10m": [180, 190, 200],
    "precipitation": [0, 0.4, 0],
    "precipitation_probability": [null, 60, 10],
    "weather_code": [3, 61, 0],
    "cloud_cover": [50, 90, 5]
  },
  "daily": {
    "time": ["2026-06-15"],
    "sunrise": ["2026-06-15T01:05"],
    "sunset": ["2026-06-15T19:50"]
  }
}`

func newClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(60.1699, 24.9384)
	c.http = srv.Client()
	c.baseURL = srv.URL
	return c
}

func TestFetchParsesTimesDailyAndNulls(t *testing.T) {
	var query url.Values
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		fmt.Fprint(w, fixture)
	}))

	d, err := c.Fetch(7, 4)
	if err != nil {
		t.Fatal(err)
	}

	if len(d.Time) != 3 || d.Time[0] != 1781517600 || d.Time[1]-d.Time[0] != 3600 {
		t.Errorf("Time = %v", d.Time)
	}
	if d.TemperatureC[0] != 10.5 || d.TemperatureC[2] != 12.0 {
		t.Errorf("TemperatureC = %v", d.TemperatureC)
	}
	// null must surface as NaN, never as a made-up zero.
	if !math.IsNaN(d.TemperatureC[1]) || !math.IsNaN(d.PrecipProbPct[0]) {
		t.Errorf("nulls not NaN: temp=%v prob=%v", d.TemperatureC[1], d.PrecipProbPct[0])
	}
	if d.WeatherCode[1] != 61 || d.CloudCoverPct[1] != 90 || d.PrecipMM[1] != 0.4 || d.WindDirDeg[2] != 200 {
		t.Errorf("hourly arrays misparsed: %+v", d)
	}
	if len(d.Daily.Time) != 1 || d.Daily.Time[0] != 1781481600 {
		t.Errorf("Daily.Time = %v", d.Daily.Time)
	}
	if d.Daily.Sunrise[0] != 1781481600+1*3600+5*60 || d.Daily.Sunset[0] != 1781481600+19*3600+50*60 {
		t.Errorf("sun times = %v / %v", d.Daily.Sunrise, d.Daily.Sunset)
	}

	want := map[string]string{
		"latitude": "60.1699", "longitude": "24.9384", "timezone": "UTC",
		"past_days": "7", "forecast_days": "4", "models": ModelName,
	}
	for k, v := range want {
		if got := strings.Join(query[k], ","); got != v {
			t.Errorf("query %s = %q, want %q (lat/lon must not be swapped)", k, got, v)
		}
	}
	for _, field := range []string{"temperature_2m", "relative_humidity_2m", "wind_speed_10m", "wind_direction_10m", "precipitation", "precipitation_probability", "weather_code", "cloud_cover"} {
		if !strings.Contains(query.Get("hourly"), field) {
			t.Errorf("hourly param %q is missing %s", query.Get("hourly"), field)
		}
	}
}

func TestFetchErrors(t *testing.T) {
	cases := map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"http status": {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, "HTTP 503"},
		"api error":   {func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"error":true,"reason":"bad model"}`) }, "bad model"},
		"empty":       {func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"hourly":{"time":[]}}`) }, "empty"},
		"bad json":    {func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{`) }, "parse"},
		"bad time":    {func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"hourly":{"time":["nope"]}}`) }, "parse time"},
	}
	for name, c := range cases {
		cl := newClient(t, c.handler)
		if _, err := cl.Fetch(1, 1); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, c.want)
		}
	}
}

func TestFetchRejectsOversizedBodies(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"hourly":{"time":["`)
		fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	if _, err := c.Fetch(1, 1); err == nil {
		t.Error("expected an error for a body over the size cap")
	}
}
