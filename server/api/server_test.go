package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/store"
)

type fakeCurrent struct {
	cur   *netatmo.Current
	ok    bool
	calls int
}

func (f *fakeCurrent) Latest() (*netatmo.Current, bool) {
	f.calls++
	return f.cur, f.ok
}

func sampleCurrent() *netatmo.Current {
	return &netatmo.Current{
		Timestamp:         1781000000,
		OutdoorAvailable:  true,
		OutdoorTemp:       16.6,
		OutdoorHumidity:   84,
		IndoorTemp:        21.3,
		Pressure:          1003.7,
		PressureTrend:     "up",
		WindSpeed:         3,
		WindAngle:         102,
		SumRain1h:         0.2,
		TodayOutdoorMinC:  11.2,
		TodayOutdoorMaxC:  17.1,
		TodayOutdoorMinAt: 1780990000,
		Modules:           []netatmo.ModuleStatus{{Type: "NAModule1", Name: "Outdoor", BatteryPercent: 80, Reachable: true, LastSeen: 1781000000}},
	}
}

func newTestServer(t *testing.T, cur *fakeCurrent, obs ...netatmo.Observation) (*Server, *store.TimeSeries) {
	t.Helper()
	ts := store.NewTimeSeries(24 * 10)
	ts.Append(obs...)
	engine := forecast.NewEngine(ts, forecast.Config{StationID: "st", StaleAfter: time.Hour})
	return NewServer("127.0.0.1:0", engine, cur, ts), ts
}

func history(hours int) []netatmo.Observation {
	now := time.Now().UTC()
	var out []netatmo.Observation
	for h := hours; h >= 1; h-- {
		out = append(out, netatmo.Observation{
			Timestamp:   now.Add(-time.Duration(h) * time.Hour).Unix(),
			Temperature: 13, Humidity: 80, WindSpeed: 5, WindAngle: 180,
		})
	}
	return out
}

func do(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, rec.Body)
	}
	return m
}

func TestHealth(t *testing.T) {
	s, _ := newTestServer(t, &fakeCurrent{})
	rec := do(t, s, http.MethodGet, "/health")
	if rec.Code != 200 || decode(t, rec)["status"] != "ok" {
		t.Errorf("/health = %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestCurrentUsesSnakeCaseWithUnitsAndNeverCallsUpstreamPerRequest(t *testing.T) {
	src := &fakeCurrent{cur: sampleCurrent(), ok: true}
	s, _ := newTestServer(t, src)

	rec := do(t, s, http.MethodGet, "/current")
	if rec.Code != 200 {
		t.Fatalf("/current = %d %s", rec.Code, rec.Body)
	}
	m := decode(t, rec)

	want := map[string]any{
		"timestamp":            "2026-06-09T10:13:20Z",
		"outdoor_available":    true,
		"outdoor_temp_c":       16.6,
		"outdoor_humidity_pct": 84.0,
		"indoor_temp_c":        21.3,
		"pressure_hpa":         1003.7,
		"pressure_trend":       "up",
		"wind_speed_kmh":       3.0,
		"wind_angle_deg":       102.0,
		"sum_rain_1h_mm":       0.2,
		"today_outdoor_min_c":  11.2,
		"today_outdoor_min_at": "2026-06-09T07:26:40Z",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, present := m["OutdoorTemp"]; present {
		t.Error("PascalCase keys leaked into the response")
	}
	if _, present := m["today_outdoor_max_at"]; present {
		t.Error("an unreported timestamp should be omitted, not serialised as 1970")
	}
	if a, _ := m["apparent_temp_c"].(float64); a == 0 {
		t.Error("apparent_temp_c missing for an available outdoor module")
	}
	mods, _ := m["modules"].([]any)
	if len(mods) != 1 || mods[0].(map[string]any)["battery_percent"] != 80.0 {
		t.Errorf("modules = %v", m["modules"])
	}

	do(t, s, http.MethodGet, "/current")
	if src.calls != 2 {
		t.Errorf("source consulted %d times for 2 requests", src.calls)
	}
}

func TestCurrentWithoutOutdoorModuleFlagsItAndOmitsApparent(t *testing.T) {
	cur := sampleCurrent()
	cur.OutdoorAvailable = false
	cur.OutdoorTemp, cur.OutdoorHumidity = 0, 0
	s, _ := newTestServer(t, &fakeCurrent{cur: cur, ok: true})

	m := decode(t, do(t, s, http.MethodGet, "/current"))
	if m["outdoor_available"] != false || m["apparent_temp_c"] != 0.0 {
		t.Errorf("outdoor_available=%v apparent_temp_c=%v", m["outdoor_available"], m["apparent_temp_c"])
	}
}

func TestCurrentBeforeAnyReadingIs503WithoutUpstreamDetail(t *testing.T) {
	s, _ := newTestServer(t, &fakeCurrent{})
	rec := do(t, s, http.MethodGet, "/current")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if msg, _ := decode(t, rec)["error"].(string); msg == "" || strings.Contains(msg, "http") {
		t.Errorf("error = %q", msg)
	}
}

func TestForecast(t *testing.T) {
	s, _ := newTestServer(t, &fakeCurrent{}, history(72)...)
	rec := do(t, s, http.MethodGet, "/forecast")
	if rec.Code != 200 {
		t.Fatalf("/forecast = %d %s", rec.Code, rec.Body)
	}
	m := decode(t, rec)
	days, _ := m["days"].([]any)
	if len(days) != 3 || m["stale"] != false || m["station_id"] != "st" {
		t.Errorf("days=%d stale=%v station=%v", len(days), m["stale"], m["station_id"])
	}
	if gen, _ := m["generated_at"].(string); strings.Contains(gen, ".") {
		t.Errorf("generated_at = %q, want whole seconds", gen)
	}
}

func TestForecastWithNoDataIsStaleWithEmptyDaysNotNull(t *testing.T) {
	s, _ := newTestServer(t, &fakeCurrent{})
	rec := do(t, s, http.MethodGet, "/forecast")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	m := decode(t, rec)
	days, ok := m["days"].([]any)
	if !ok || len(days) != 0 || m["stale"] != true {
		t.Errorf("days=%#v stale=%v, want [] and true", m["days"], m["stale"])
	}
}

func TestDebug(t *testing.T) {
	empty, _ := newTestServer(t, &fakeCurrent{})
	m := decode(t, do(t, empty, http.MethodGet, "/debug"))
	if samples, ok := m["sample_temps_last_5"].([]any); !ok || len(samples) != 0 {
		t.Errorf("empty store sample_temps_last_5 = %#v, want []", m["sample_temps_last_5"])
	}

	full, _ := newTestServer(t, &fakeCurrent{}, history(10)...)
	m = decode(t, do(t, full, http.MethodGet, "/debug"))
	if m["observation_count"] != 10.0 || len(m["sample_temps_last_5"].([]any)) != 5 {
		t.Errorf("debug = %v", m)
	}
}

func TestUnknownRoutesAndWrongMethodsAreJSON(t *testing.T) {
	s, _ := newTestServer(t, &fakeCurrent{})

	rec := do(t, s, http.MethodGet, "/nope")
	if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/json" || decode(t, rec)["error"] == "" {
		t.Errorf("GET /nope = %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}

	rec = do(t, s, http.MethodPost, "/forecast")
	if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" || decode(t, rec)["error"] == "" {
		t.Errorf("POST /forecast = %d allow=%q %s", rec.Code, rec.Header().Get("Allow"), rec.Body)
	}

	if rec = do(t, s, http.MethodHead, "/health"); rec.Code != 200 {
		t.Errorf("HEAD /health = %d", rec.Code)
	}
}

func TestListenReportsAPortConflictImmediately(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()

	s, _ := newTestServer(t, &fakeCurrent{})
	s.addr = taken.Addr().String()
	if ln, err := s.Listen(); err == nil {
		ln.Close()
		t.Fatal("Listen succeeded on a port that is already in use")
	}
}

func TestServeAnswersRequestsAndShutsDownCleanly(t *testing.T) {
	s, _ := newTestServer(t, &fakeCurrent{})
	ln, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "ok") {
		t.Errorf("GET /health = %d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v after a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}
