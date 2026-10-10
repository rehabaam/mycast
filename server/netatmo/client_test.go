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
         "dashboard_data": {"time_utc": 1780999900, "Temperature": 16.6, "Humidity": 84, "min_temp": 11.2, "max_temp": 17.1, "date_min_temp": 1780990000, "date_max_temp": 1780995000}},
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
	c := NewClient(srv.Client(), Config{StationID: station, OutdoorModuleID: outdoor, WindModuleID: wind, RainModuleID: rain})
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
	// The outdoor module's own measurement time is kept apart from the base's.
	if cur.OutdoorTimestamp != 1780999900 {
		t.Errorf("OutdoorTimestamp = %d, want 1780999900 (the module's time_utc, not the base's 1781000000)", cur.OutdoorTimestamp)
	}
	if !cur.WindAvailable {
		t.Error("WindAvailable = false, want true")
	}
	if cur.PressureTrend != "up" || cur.TempTrend != "stable" {
		t.Errorf("trends = %q/%q, want up/stable", cur.PressureTrend, cur.TempTrend)
	}
	if len(cur.Modules) != 3 || cur.Modules[0].BatteryPercent != 80 || !cur.Modules[0].Reachable {
		t.Errorf("Modules = %+v", cur.Modules)
	}
}

func TestGetCurrentDescribesTheStation(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "", "", "", "")

	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}
	st := cur.Station

	if st.ID != "70:ee:50:00:00:01" {
		t.Errorf("ID = %q", st.ID)
	}
	// Netatmo reports [longitude, latitude].
	if !st.HasLocation || st.Lat != 60.17 || st.Lon != 24.94 {
		t.Errorf("location = (%v, %v, %v), want (60.17, 24.94, true) — lat/lon must not be swapped", st.Lat, st.Lon, st.HasLocation)
	}
	if st.Timezone != "Europe/Helsinki" {
		t.Errorf("Timezone = %q", st.Timezone)
	}
	if st.OutdoorModuleID != "02:00:00:00:00:01" || st.WindModuleID != "06:00:00:00:00:01" || st.RainModuleID != "05:00:00:00:00:01" {
		t.Errorf("module IDs = %q/%q/%q", st.OutdoorModuleID, st.WindModuleID, st.RainModuleID)
	}
}

func TestConfiguredIDsOverrideDiscoveredOnes(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "70:ee:50:99:99:99", "", "06:00:00:00:00:01", "")

	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if cur.Station.ID != "70:ee:50:99:99:99" {
		t.Errorf("ID = %q, want the configured one", cur.Station.ID)
	}
	if cur.Station.OutdoorModuleID != "02:00:00:00:00:01" {
		t.Errorf("OutdoorModuleID = %q, want the discovered one where none is configured", cur.Station.OutdoorModuleID)
	}
}

// The client keeps nothing between calls: what it learns comes back in the
// Station value, so there is no required order of calls.
func TestClientCarriesNoStateBetweenCalls(t *testing.T) {
	var gotDeviceIDs []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDeviceIDs = append(gotDeviceIDs, r.URL.Query().Get("device_id"))
		fmt.Fprint(w, stationsFixture)
	})
	c := newTestClient(t, h, "", "", "", "")

	for i := 0; i < 2; i++ {
		if _, err := c.GetCurrent(); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range gotDeviceIDs {
		if id != "" {
			t.Errorf("request %d sent device_id=%q: a discovered ID must not leak into later calls", i, id)
		}
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
			"dashboard_data": map[string]any{"time_utc": 1780999800, "Temperature": 5.5, "Humidity": 70},
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

func TestObservationLeavesRainToTheHourlyHistory(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "", "", "", "")
	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}

	obs, ok := cur.Observation()
	if !ok {
		t.Fatal("Observation() not ok")
	}
	// The station's rolling last-hour sum (0.2 mm) straddles two clock hours,
	// so storing it in one hour's bucket would double-count rain that the
	// completed hour already holds. Neither it nor the instantaneous 0.1 mm
	// may leak into the observation.
	if cur.SumRain1h != 0.2 {
		t.Fatalf("fixture SumRain1h = %v, want 0.2", cur.SumRain1h)
	}
	if obs.Rain != 0 {
		t.Errorf("Rain = %v, want 0 (the clock-hour total comes from history)", obs.Rain)
	}
	// Stamped with the outdoor module's own time, not the base station's.
	if obs.Timestamp != 1780999900 || obs.Temperature != 16.6 || obs.Humidity != 84 || obs.WindSpeed != 3 || obs.WindAngle != 102 {
		t.Errorf("Observation = %+v", obs)
	}
	if want := FieldOutdoor | FieldWind; obs.Has != want {
		t.Errorf("Has = %b, want %b (outdoor + wind, no rain)", obs.Has, want)
	}
}

func TestObservationOmitsWindWhenTheWindModuleIsOffline(t *testing.T) {
	body := stationsWith(t, func(dev map[string]any) {
		moduleOfType(dev, "NAModule2")["reachable"] = false
	})
	c := newTestClient(t, serveStations(body), "", "", "", "")
	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}

	if cur.WindAvailable {
		t.Error("WindAvailable = true for an unreachable wind module")
	}
	obs, ok := cur.Observation()
	if !ok {
		t.Fatal("outdoor is fine; Observation() should be ok")
	}
	if obs.Has != FieldOutdoor {
		t.Errorf("Has = %b, want outdoor only, so a stored wind value isn't overwritten with a calm", obs.Has)
	}
}

// A module that has gone quiet leaves its last values on the dashboard while
// the base station keeps ticking, so the reading has to carry the module's
// time, not the base's.
func TestQuietOutdoorModuleKeepsItsOwnOldTimestamp(t *testing.T) {
	body := stationsWith(t, func(dev map[string]any) {
		// The base reports "now" (1781000000); the outdoor module last
		// measured three hours earlier but is still flagged reachable.
		moduleOfType(dev, "NAModule1")["dashboard_data"].(map[string]any)["time_utc"] = float64(1781000000 - 3*3600)
	})
	c := newTestClient(t, serveStations(body), "", "", "", "")
	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}

	obs, ok := cur.Observation()
	if !ok {
		t.Fatal("Observation() not ok")
	}
	if obs.Timestamp != 1781000000-3*3600 {
		t.Errorf("Timestamp = %d, want the module's three-hours-old time, not the base's", obs.Timestamp)
	}
	if cur.Timestamp != 1781000000 {
		t.Errorf("base Timestamp = %d, want it untouched", cur.Timestamp)
	}
}

func TestOutdoorTimeFallsBackToLastSeenAndOtherwiseIsUnavailable(t *testing.T) {
	// No time_utc in the module's dashboard data: use when the base last
	// heard from it.
	body := stationsWith(t, func(dev map[string]any) {
		delete(moduleOfType(dev, "NAModule1")["dashboard_data"].(map[string]any), "time_utc")
	})
	c := newTestClient(t, serveStations(body), "", "", "", "")
	cur, err := c.GetCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if !cur.OutdoorAvailable || cur.OutdoorTimestamp != 1781000000 {
		t.Errorf("available=%v OutdoorTimestamp=%d, want true / last_seen 1781000000", cur.OutdoorAvailable, cur.OutdoorTimestamp)
	}

	// Neither: the reading can't be dated, so it isn't used.
	body = stationsWith(t, func(dev map[string]any) {
		mod := moduleOfType(dev, "NAModule1")
		delete(mod["dashboard_data"].(map[string]any), "time_utc")
		delete(mod, "last_seen")
	})
	c = newTestClient(t, serveStations(body), "", "", "", "")
	if cur, err = c.GetCurrent(); err != nil {
		t.Fatal(err)
	}
	if cur.OutdoorAvailable {
		t.Error("OutdoorAvailable = true for a reading with no measurement time at all")
	}
}

func TestGetCurrentRecordsWhenItWasFetched(t *testing.T) {
	c := newTestClient(t, serveStations(stationsFixture), "", "", "", "")

	before := time.Now()
	cur, err := c.GetCurrent()
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if cur.FetchedAt.Before(before) || cur.FetchedAt.After(after) {
		t.Errorf("FetchedAt = %v, want within [%v, %v]", cur.FetchedAt, before, after)
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
	obs, err := c.GetMeasures("70:ee:50:00:00:01", "02:00:00:00:00:01", []string{"Temperature", "Humidity"}, from, to)
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
	for i, o := range obs {
		if o.Has != FieldOutdoor {
			t.Errorf("obs[%d].Has = %b, want outdoor", i, o.Has)
		}
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
	c := newTestClient(t, h, "", "", "", "")
	st := Station{ID: "70:ee:50:00:00:01", OutdoorModuleID: "02:00:00:00:00:01", WindModuleID: "06:00:00:00:00:01", RainModuleID: "05:00:00:00:00:01"}

	obs, err := c.GetHistory(st, time.Unix(1780000000, 0), time.Unix(1780010000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2", len(obs))
	}
	first, second := obs[0], obs[1]
	if first.Rain != 0.4 {
		t.Errorf("first = %+v: rain 0.4", first)
	}
	if second.WindSpeed != 7 || second.WindAngle != 200 || second.GustSpeed != 15 || second.GustAngle != 210 {
		t.Errorf("second = %+v: want the wind sample merged in", second)
	}

	// There is no wind aggregate for the first hour. It must say so, rather
	// than present a 0 km/h calm as if it had been measured.
	if first.Has != FieldOutdoor|FieldRain {
		t.Errorf("first.Has = %b, want outdoor+rain (no wind)", first.Has)
	}
	if second.Has != FieldsAll {
		t.Errorf("second.Has = %b, want all groups", second.Has)
	}
}

func TestGetHistoryTreatsNullWindAsMissingNotCalm(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.PostForm.Get("module_id") {
		case "02:00:00:00:00:01":
			fmt.Fprint(w, `{"body":[{"beg_time":1780000800,"step_time":3600,"value":[[10,80],[11,81],[12,82]]}]}`)
		case "06:00:00:00:00:01":
			// Hour 0: wind null. Hour 1: a real, genuine calm. Hour 2: the
			// gust half of the row is null, so the group is incomplete.
			fmt.Fprint(w, `{"body":[{"beg_time":1780000800,"step_time":3600,"value":[[null,null,null,null],[0,0,0,0],[6,90,null,null]]}]}`)
		}
	})
	c := newTestClient(t, h, "", "", "", "")
	st := Station{ID: "70:ee:50:00:00:01", OutdoorModuleID: "02:00:00:00:00:01", WindModuleID: "06:00:00:00:00:01"}

	obs, err := c.GetHistory(st, time.Unix(1780000000, 0), time.Unix(1780010000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 3 {
		t.Fatalf("got %d observations, want 3", len(obs))
	}
	wantHas := []Fields{FieldOutdoor, FieldOutdoor | FieldWind, FieldOutdoor}
	for i, want := range wantHas {
		if obs[i].Has != want {
			t.Errorf("obs[%d].Has = %b, want %b", i, obs[i].Has, want)
		}
	}
	// A real calm is a measurement; it is kept.
	if obs[1].WindSpeed != 0 || obs[1].Has&FieldWind == 0 {
		t.Errorf("obs[1] = %+v: a genuine 0 km/h must stay a measured calm", obs[1])
	}
}

func TestGetMeasuresTreatsShortRowsAsMissing(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The second row has temperature only; humidity is missing entirely.
		fmt.Fprint(w, `{"body":[{"beg_time":1780000800,"step_time":3600,"value":[[10,80],[11]]}]}`)
	})
	c := newTestClient(t, h, "", "", "", "")

	obs, err := c.GetMeasures("70:ee:50:00:00:01", "02:00:00:00:00:01", []string{"Temperature", "Humidity"}, time.Unix(1780000000, 0), time.Unix(1780010000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 {
		t.Fatalf("got %d observations %+v, want only the complete row", len(obs), obs)
	}
}

func TestGetHistoryRefusesAStationWithNoOutdoorModule(t *testing.T) {
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	c := newTestClient(t, h, "", "", "", "")

	// Without a module ID, getmeasure would answer for the indoor base
	// station, which would then be stored as outdoor history.
	_, err := c.GetHistory(Station{ID: "70:ee:50:00:00:01"}, time.Unix(1780000000, 0), time.Unix(1780010000, 0))
	if err == nil {
		t.Fatal("expected an error")
	}
	if called {
		t.Error("the API was called without an outdoor module to ask about")
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
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getmeasure") {
			fmt.Fprint(w, `{"body":[{"beg_time":1780000800,"step_time":3600,"value":[[10,80]]}]}`)
			return
		}
		fmt.Fprint(w, stationsFixture)
	})
	c := newTestClient(t, h, "", "", "", "")
	st := Station{ID: "70:ee:50:00:00:01", OutdoorModuleID: "02:00:00:00:00:01"}

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
			for j := 0; j < 20; j++ {
				if _, err := c.GetHistory(st, time.Unix(1780000000, 0), time.Unix(1780010000, 0)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
