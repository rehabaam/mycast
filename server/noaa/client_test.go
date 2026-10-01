package noaa

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient()
	c.http = srv.Client()
	c.url = srv.URL
	return c
}

func TestFetchKpForecastParsesPoints(t *testing.T) {
	c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
		  {"time_tag":"2026-06-15T00:00:00","kp":2.33,"observed":"observed","noaa_scale":null},
		  {"time_tag":"2026-06-15T03:00:00","kp":5.67,"observed":"predicted","noaa_scale":"G1"}
		]`)
	}))

	pts, err := c.FetchKpForecast()
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("got %d points", len(pts))
	}
	if pts[0].Time != 1781481600 || pts[0].Kp != 2.33 {
		t.Errorf("pts[0] = %+v", pts[0])
	}
	if pts[1].Time-pts[0].Time != 3*3600 || pts[1].Kp != 5.67 {
		t.Errorf("pts[1] = %+v", pts[1])
	}
}

func TestFetchKpForecastErrors(t *testing.T) {
	cases := map[string]struct {
		h    http.HandlerFunc
		want string
	}{
		"status":   {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, "HTTP 500"},
		"empty":    {func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) }, "empty"},
		"bad json": {func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{`) }, "parse"},
		"bad time": {func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[{"time_tag":"yesterday","kp":1}]`) }, "parse time"},
	}
	for name, c := range cases {
		cl := newClient(t, c.h)
		if _, err := cl.FetchKpForecast(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, c.want)
		}
	}
}
