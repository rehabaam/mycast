package netatmo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stationsFixture mirrors the shape of a real getstationsdata response
// (values are made up; the location is central Helsinki, not any real
// station).
const stationsFixture = `{
  "status": "ok",
  "body": {
    "devices": [{
      "_id": "70:ee:50:00:00:01",
      "station_name": "Home",
      "dashboard_data": {
        "time_utc": 1781000000,
        "Temperature": 21.3, "Humidity": 60, "Pressure": 1003.7,
        "temp_trend": "stable", "pressure_trend": "up"
      },
      "place": {"location": [24.94, 60.17], "city": "Helsinki", "country": "FI", "altitude": 10, "timezone": "Europe/Helsinki"},
      "modules": [
        {"_id": "02:00:00:00:00:01", "type": "NAModule1", "module_name": "Outdoor", "battery_percent": 80, "reachable": true, "rf_status": 60, "last_seen": 1781000000,
         "dashboard_data": {"Temperature": 16.6, "Humidity": 84, "min_temp": 11.2, "max_temp": 17.1, "date_min_temp": 1780990000, "date_max_temp": 1780995000}},
        {"_id": "06:00:00:00:00:01", "type": "NAModule2", "module_name": "Wind", "battery_percent": 90, "reachable": true, "rf_status": 70, "last_seen": 1781000000,
         "dashboard_data": {"WindStrength": 3, "WindAngle": 102, "GustStrength": 13, "GustAngle": 110}},
        {"_id": "05:00:00:00:00:01", "type": "NAModule3", "module_name": "Rain", "battery_percent": 70, "reachable": true, "rf_status": 65, "last_seen": 1781000000,
         "dashboard_data": {"Rain": 0.1, "sum_rain_1": 0.2, "sum_rain_24": 10.1}}
      ]
    }]
  }
}`

// stationsWith returns the fixture after letting edit modify the device.
func stationsWith(t *testing.T, edit func(dev map[string]any)) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(stationsFixture), &doc); err != nil {
		t.Fatal(err)
	}
	dev := doc["body"].(map[string]any)["devices"].([]any)[0].(map[string]any)
	edit(dev)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func moduleOfType(dev map[string]any, typ string) map[string]any {
	for _, m := range dev["modules"].([]any) {
		if mod := m.(map[string]any); mod["type"] == typ {
			return mod
		}
	}
	return nil
}

func newTestClient(t *testing.T, h http.Handler, station, outdoor, wind, rain string) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.Client(), station, outdoor, wind, rain)
	c.baseURL = srv.URL
	return c
}

func serveStations(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	})
}

func TestGetCurrentParsesReadings(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "", "", "", "")

	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}

	if !cur.OutdoorAvailable {
		t.Error("OutdoorAvailable = false, want true")
	}
	checks := []struct {
		name      string
		got, want float64
	}{
		{"OutdoorTemp", cur.OutdoorTemp, 16.6},
		{"OutdoorHumidity", cur.OutdoorHumidity, 84},
		{"IndoorTemp", cur.IndoorTemp, 21.3},
		{"IndoorHumidity", cur.IndoorHumidity, 60},
		{"Pressure", cur.Pressure, 1003.7},
		{"WindSpeed", cur.WindSpeed, 3},
		{"WindAngle", cur.WindAngle, 102},
		{"GustSpeed", cur.GustSpeed, 13},
		{"GustAngle", cur.GustAngle, 110},
		{"Rain", cur.Rain, 0.1},
		{"SumRain1h", cur.SumRain1h, 0.2},
		{"SumRain24h", cur.SumRain24h, 10.1},
		{"TodayOutdoorMinC", cur.TodayOutdoorMinC, 11.2},
		{"TodayOutdoorMaxC", cur.TodayOutdoorMaxC, 17.1},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
	if cur.Timestamp != 1781000000 || cur.TodayOutdoorMinAt != 1780990000 || cur.TodayOutdoorMaxAt != 1780995000 {
		t.Errorf("timestamps = %d/%d/%d", cur.Timestamp, cur.TodayOutdoorMinAt, cur.TodayOutdoorMaxAt)
	}
	if cur.PressureTrend != "up" || cur.TempTrend != "stable" {
		t.Errorf("trends = %q/%q, want up/stable", cur.PressureTrend, cur.TempTrend)
	}
	if len(cur.Modules) != 3 || cur.Modules[0].BatteryPercent != 80 || !cur.Modules[0].Reachable {
		t.Errorf("Modules = %+v", cur.Modules)
	}
}

func TestGetCurrentDiscoversIDsLocationAndTimezone(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "", "", "", "")

	if _, _, ok := c.Location(); ok {
		t.Fatal("Location ok before any GetCurrent")
	}
	if c.StationID() != "" || c.Timezone() != "" {
		t.Fatal("StationID/Timezone set before any GetCurrent")
	}

	if _, err := c.GetCurrent(); err != nil {
		t.Fatal(err)
	}

	if got := c.StationID(); got != "70:ee:50:00:00:01" {
		t.Errorf("StationID = %q", got)
	}
	// Netatmo reports [longitude, latitude].
	lat, lon, ok := c.Location()
	if !ok || lat != 60.17 || lon != 24.94 {
		t.Errorf("Location = (%v, %v, %v), want (60.17, 24.94, true) — lat/lon must not be swapped", lat, lon, ok)
	}
	if got := c.Timezone(); got != "Europe/Helsinki" {
		t.Errorf("Timezone = %q", got)
	}
	_, outdoor, wind, rain := c.ids()
	if outdoor != "02:00:00:00:00:01" || wind != "06:00:00:00:00:01" || rain != "05:00:00:00:00:01" {
		t.Errorf("module IDs = %q/%q/%q", outdoor, wind, rain)
	}
}

func TestGetCurrentSendsTheConfiguredStationID(t *testing.T) {
	var got string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("device_id")
		fmt.Fprint(w, stationsFixture)
	})
	c := newTestClient(t, h, "70:ee:50:99:99:99", "", "", "")
	if _, err := c.GetCurrent(); err != nil {
		t.Fatal(err)
	}
	if got != "70:ee:50:99:99:99" {
		t.Errorf("device_id = %q", got)
	}
}

func TestGetCurrentOutdoorModuleUnreachable(t *testing.T) {
	body := stationsWith(t, func(dev map[string]any) {
		moduleOfType(dev, "NAModule1")["reachable"] = false
	})
	c := newTestClient(t, serveStations(body), "", "", "", "")

	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if cur.OutdoorAvailable {
		t.Error("OutdoorAvailable = true for an unreachable module")
	}
	if _, ok := cur.Observation(); ok {
		t.Error("Observation() ok for an unreachable outdoor module: a fake 0 °C reading would be stored")
	}
	// Other modules are unaffected, and every module is still reported.
	if cur.WindSpeed != 3 || len(cur.Modules) != 3 {
		t.Errorf("WindSpeed=%v modules=%d", cur.WindSpeed, len(cur.Modules))
	}
}

func TestGetCurrentOutdoorModuleWithoutDashboardData(t *testing.T) {
	body := stationsWith(t, func(dev map[string]any) {
		delete(moduleOfType(dev, "NAModule1"), "dashboard_data")
	})
	c := newTestClient(t, serveStations(body), "", "", "", "")

	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if cur.OutdoorAvailable {
		t.Error("OutdoorAvailable = true with no dashboard_data")
	}
}

func TestGetCurrentWithoutAnOutdoorModule(t *testing.T) {
	body := stationsWith(t, func(dev map[string]any) {
		var kept []any
		for _, m := range dev["modules"].([]any) {
			if m.(map[string]any)["type"] != "NAModule1" {
				kept = append(kept, m)
			}
		}
		dev["modules"] = kept
	})
	c := newTestClient(t, serveStations(body), "", "", "", "")

	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if cur.OutdoorAvailable {
		t.Error("OutdoorAvailable = true with no outdoor module")
	}
}

func TestGetCurrentReadsTheConfiguredModule(t *testing.T) {
	body := stationsWith(t, func(dev map[string]any) {
		second := map[string]any{
			"_id": "02:00:00:00:00:02", "type": "NAModule1", "module_name": "Shed", "reachable": true,
			"dashboard_data": map[string]any{"Temperature": 5.5, "Humidity": 70},
		}
		dev["modules"] = append(dev["modules"].([]any), second)
	})

	// With no configured ID the first outdoor module wins (the same one
	// history uses)...
	c := newTestClient(t, serveStations(body), "", "", "", "")
	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if cur.OutdoorTemp != 16.6 {
		t.Errorf("auto-discovered outdoor temp = %v, want 16.6 (first module)", cur.OutdoorTemp)
	}

	// ...and a configured ID selects its own module.
	c = newTestClient(t, serveStations(body), "", "02:00:00:00:00:02", "", "")
	cur, err = c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if cur.OutdoorTemp != 5.5 {
		t.Errorf("configured outdoor temp = %v, want 5.5 (second module)", cur.OutdoorTemp)
	}
}

func TestObservationUsesTheHourlyRainSum(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "", "", "", "")
	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}

	obs, ok := cur.Observation()
	if !ok {
		t.Fatal("Observation() not ok")
	}
	// Rain is 0.1 (the latest sample) but sum_rain_1 is 0.2 mm/h, which is
	// the unit of the hourly history.
	if obs.Rain != 0.2 {
		t.Errorf("Rain = %v, want 0.2 (sum over the hour)", obs.Rain)
	}
	if obs.Timestamp != 1781000000 || obs.Temperature != 16.6 || obs.Humidity != 84 || obs.WindSpeed != 3 || obs.WindAngle != 102 {
		t.Errorf("Observation = %+v", obs)
	}
}

func TestLatest(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "", "", "", "")

	if cur, ok := c.Latest(); ok || cur != nil {
		t.Fatalf("Latest before any fetch = (%v, %v), want (nil, false)", cur, ok)
	}

	if _, err := c.GetCurrent(); err != nil {
		t.Fatal(err)
	}
	cur, ok := c.Latest()
	if !ok || cur.OutdoorTemp != 16.6 {
		t.Fatalf("Latest = (%+v, %v)", cur, ok)
	}

	// Callers get a copy, so they can't corrupt what other readers see.
	cur.OutdoorTemp = 99
	cur.Modules[0].Name = "mutated"
	again, _ := c.Latest()
	if again.OutdoorTemp != 16.6 || again.Modules[0].Name != "Outdoor" {
		t.Errorf("Latest was mutated through a returned copy: %+v", again)
	}
}

func TestFailedFetchKeepsThePreviousLatest(t *testing.T) {
	fail := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, stationsFixture)
	})
	c := newTestClient(t, h, "", "", "", "")
	if _, err := c.GetCurrent(); err != nil {
		t.Fatal(err)
	}

	fail = true
	if _, err := c.GetCurrent(); err == nil {
		t.Fatal("GetCurrent succeeded against a failing upstream")
	}
	if cur, ok := c.Latest(); !ok || cur.OutdoorTemp != 16.6 {
		t.Errorf("Latest after a failed fetch = (%+v, %v), want the earlier reading", cur, ok)
	}
}

func TestGetCurrentRejectsAnEmptyStationList(t *testing.T) {
	c := newTestClient(t, serveStations(`{"status":"ok","body":{"devices":[]}}`), "", "", "", "")
	if _, err := c.GetCurrent(); err == nil {
		t.Fatal("expected an error when no stations are returned")
	}
}

func TestGetMeasuresParsesStepsAndSkipsMissingTemperature(t *testing.T) {
	var form map[string][]string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = r.PostForm
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/getmeasure") {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"status":"ok","body":[{"beg_time":1780000800,"step_time":3600,"value":[[10.5,80],[null,null],[11.25,79]]}]}`)
	})
	c := newTestClient(t, h, "70:ee:50:00:00:01", "", "", "")

	from := time.Unix(1780000000, 0)
	to := time.Unix(1780010000, 0)
	obs, err := c.GetMeasures("02:00:00:00:00:01", []string{"Temperature", "Humidity"}, from, to)
	if err != nil {
		t.Fatal(err)
	}

	// The middle step has no data: it must be dropped, not stored as 0 °C.
	if len(obs) != 2 {
		t.Fatalf("got %d observations %+v, want 2", len(obs), obs)
	}
	if obs[0].Timestamp != 1780000800 || obs[0].Temperature != 10.5 || obs[0].Humidity != 80 {
		t.Errorf("obs[0] = %+v", obs[0])
	}
	if obs[1].Timestamp != 1780000800+2*3600 || obs[1].Temperature != 11.25 || obs[1].Humidity != 79 {
		t.Errorf("obs[1] = %+v", obs[1])
	}

	want := map[string]string{
		"device_id":  "70:ee:50:00:00:01",
		"module_id":  "02:00:00:00:00:01",
		"scale":      "1hour",
		"type":       "Temperature,Humidity",
		"date_begin": "1780000000",
		"date_end":   "1780010000",
	}
	for k, v := range want {
		if got := strings.Join(form[k], ","); got != v {
			t.Errorf("form %s = %q, want %q", k, got, v)
		}
	}
}

func TestGetHistoryMergesWindAndRainByTimestamp(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.PostForm.Get("module_id") {
		case "02:00:00:00:00:01": // outdoor: two hours
			fmt.Fprint(w, `{"body":[{"beg_time":1780000800,"step_time":3600,"value":[[10,80],[11,81]]}]}`)
		case "06:00:00:00:00:01": // wind: second hour only (offset by one step)
			fmt.Fprint(w, `{"body":[{"beg_time":1780004400,"step_time":3600,"value":[[7,200,15,210]]}]}`)
		case "05:00:00:00:00:01": // rain: both hours
			fmt.Fprint(w, `{"body":[{"beg_time":1780000800,"step_time":3600,"value":[[0.4],[0]]}]}`)
		default:
			t.Errorf("unexpected module %q", r.PostForm.Get("module_id"))
		}
	})
	c := newTestClient(t, h, "70:ee:50:00:00:01", "02:00:00:00:00:01", "06:00:00:00:00:01", "05:00:00:00:00:01")

	obs, err := c.GetHistory(time.Unix(1780000000, 0), time.Unix(1780010000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2", len(obs))
	}
	first, second := obs[0], obs[1]
	if first.WindSpeed != 0 || first.Rain != 0.4 {
		t.Errorf("first = %+v: no wind sample at that hour, rain 0.4", first)
	}
	if second.WindSpeed != 7 || second.WindAngle != 200 || second.GustSpeed != 15 || second.GustAngle != 210 {
		t.Errorf("second = %+v: want the wind sample merged in", second)
	}
}

func TestHTTPErrorsAreReportedWithATruncatedBody(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, strings.Repeat("x", 100_000))
	})
	c := newTestClient(t, h, "", "", "", "")

	_, err := c.GetCurrent()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error %q does not mention the status", err)
	}
	if len(err.Error()) > 1000 {
		t.Errorf("error is %d bytes: the upstream body should be truncated", len(err.Error()))
	}
}

func TestOversizedResponsesAreRejected(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok","body":{"devices":[],"pad":"`)
		fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
		fmt.Fprint(w, `"}}`)
	})
	c := newTestClient(t, h, "", "", "", "")

	_, err := c.GetCurrent()
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want a size-limit error", err)
	}
}

func TestClientIsSafeForConcurrentUse(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "", "", "", "")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := c.GetCurrent(); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.Latest()
				c.Location()
				c.StationID()
				c.Timezone()
			}
		}()
	}
	wg.Wait()
}
