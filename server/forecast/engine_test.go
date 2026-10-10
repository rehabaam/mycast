package forecast

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/noaa"
	"github.com/rehabaam/mycast/openmeteo"
	"github.com/rehabaam/mycast/store"
)

// testNow is the fixed instant tests treat as "now", so nothing depends on
// when (or at which hour boundary) the suite happens to run.
var testNow = time.Date(2026, time.June, 15, 12, 30, 0, 0, time.UTC)

// fairbanks is a public, well-documented aurora reference point (~65.6°
// geomagnetic latitude, needs only Kp ~1-2), chosen because it isn't tied to
// any particular station's real location.
const fairbanksLat, fairbanksLon = 64.84, -147.72

// newTestEngine builds an Engine whose clock is frozen at testNow.
func newTestEngine(ts *store.TimeSeries, cfg Config) *Engine {
	e := NewEngine(ts, cfg)
	e.now = func() time.Time { return testNow }
	return e
}

// fakeOMClient lets tests control Open-Meteo's response without a network
// call.
type fakeOMClient struct {
	data *openmeteo.HourlyData
	err  error
}

func (f *fakeOMClient) Fetch(pastDays, forecastDays int) (*openmeteo.HourlyData, error) {
	return f.data, f.err
}

// fakeKpClient lets tests control NOAA's response without a network call.
type fakeKpClient struct {
	points []noaa.KpPoint
	err    error
}

func (f *fakeKpClient) FetchKpForecast() ([]noaa.KpPoint, error) {
	return f.points, f.err
}

// syntheticHourlyData builds a flat, constant-valued hourly series anchored
// to now, with sunrise at 04:00 and sunset at 20:00 UTC every day.
func syntheticHourlyData(now time.Time, pastDays, forecastDays int) *openmeteo.HourlyData {
	hour := now.UTC().Truncate(time.Hour)
	start := hour.Add(-time.Duration(pastDays) * 24 * time.Hour)
	total := (pastDays + forecastDays) * 24

	d := &openmeteo.HourlyData{}
	for i := 0; i < total; i++ {
		t := start.Add(time.Duration(i) * time.Hour)
		d.Time = append(d.Time, t.Unix())
		d.TemperatureC = append(d.TemperatureC, 10.0)
		d.HumidityPct = append(d.HumidityPct, 80.0)
		d.WindSpeedKmh = append(d.WindSpeedKmh, 5.0)
		d.WindDirDeg = append(d.WindDirDeg, 180.0)
		d.PrecipMM = append(d.PrecipMM, 0.0)
		d.PrecipProbPct = append(d.PrecipProbPct, 0.0)
		d.WeatherCode = append(d.WeatherCode, 3.0) // "Overcast"
		d.CloudCoverPct = append(d.CloudCoverPct, 50.0)
	}

	day0 := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < pastDays+forecastDays+1; i++ {
		day := day0.AddDate(0, 0, i)
		d.Daily.Time = append(d.Daily.Time, day.Unix())
		d.Daily.Sunrise = append(d.Daily.Sunrise, day.Add(4*time.Hour).Unix())
		d.Daily.Sunset = append(d.Daily.Sunset, day.Add(20*time.Hour).Unix())
	}
	return d
}

// stationHistory appends `hours` of constant-valued observations ending in
// the hour before now, aligned to whole-hour boundaries like real station
// data.
func stationHistory(ts *store.TimeSeries, now time.Time, hours int, temp, humidity, windSpeed float64) {
	for h := hours; h >= 1; h-- {
		obsTime := now.UTC().Add(-time.Duration(h) * time.Hour).Truncate(time.Hour)
		ts.Append(netatmo.Observation{
			Timestamp:   obsTime.Unix(),
			Temperature: temp,
			Humidity:    humidity,
			WindSpeed:   windSpeed,
			WindAngle:   180,
		})
	}
}

// obsAtTime builds an observation at an exact instant.
func obsAtTime(at time.Time, temp, humidity, windSpeed float64) netatmo.Observation {
	return netatmo.Observation{
		Timestamp:   at.Unix(),
		Temperature: temp,
		Humidity:    humidity,
		WindSpeed:   windSpeed,
		WindAngle:   180,
	}
}

func TestEngineUsesECMWFWithBiasCorrectionWhenAvailable(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	// Station consistently reads 3°C warmer than the "model" (a flat 10°C)
	// at every hour over the past 3 days — plenty of samples per hour-of-day
	// bucket for the bias fit to trust.
	stationHistory(ts, testNow, 72, 13.0, 80, 5.0)

	fake := &fakeOMClient{data: syntheticHourlyData(testNow, defaultPastDays, omForecastDaysBuffer)}
	engine := newTestEngine(ts, Config{StationID: "test-station", OpenMeteo: fake, StaleAfter: time.Hour})

	fc := engine.Compute()
	wantModel := openmeteo.ModelName + "+local-bias-correction"
	if fc.Model != wantModel {
		t.Fatalf("Model = %q, want %q", fc.Model, wantModel)
	}
	if fc.StationID != "test-station" {
		t.Errorf("StationID = %q, want %q", fc.StationID, "test-station")
	}

	// The bias-corrected forecast should track ~13°C (10 + learned +3°
	// bias), not the raw uncorrected model output of 10°C.
	firstHourTemp := fc.Days[0].Temperature.Hourly[0].Value
	if firstHourTemp < 12.5 || firstHourTemp > 13.5 {
		t.Errorf("first hour temp = %.2f, want ~13.0 (bias-corrected)", firstHourTemp)
	}
}

func TestEngineLabelsHoursAndDaysFromNow(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, testNow, 72, 13.0, 80, 5.0)
	fake := &fakeOMClient{data: syntheticHourlyData(testNow, defaultPastDays, omForecastDaysBuffer)}
	engine := newTestEngine(ts, Config{OpenMeteo: fake, StaleAfter: time.Hour})

	fc := engine.Compute()

	if got, want := fc.GeneratedAt, testNow.Truncate(time.Second); !got.Equal(want) {
		t.Errorf("GeneratedAt = %v, want %v", got, want)
	}
	if len(fc.Days) != forecastDays {
		t.Fatalf("got %d days, want %d", len(fc.Days), forecastDays)
	}
	// testNow is 12:30 UTC, so the first forecast hour is 13:00 and today
	// has 11 hours left.
	if got := fc.Days[0].Temperature.Hourly[0].Time; !got.Equal(firstForecastHour(testNow)) {
		t.Errorf("first hour = %v, want %v", got, firstForecastHour(testNow))
	}
	if got := len(fc.Days[0].Temperature.Hourly); got != 11 {
		t.Errorf("today has %d hours, want 11", got)
	}
	wantDates := []string{"2026-06-15", "2026-06-16", "2026-06-17"}
	for i, want := range wantDates {
		if fc.Days[i].Date != want {
			t.Errorf("Days[%d].Date = %q, want %q", i, fc.Days[i].Date, want)
		}
	}
}

func TestEngineDaysFollowStationTimezone(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, testNow, 72, 13.0, 80, 5.0)
	fake := &fakeOMClient{data: syntheticHourlyData(testNow, defaultPastDays, omForecastDaysBuffer)}
	// UTC+14: testNow (12:30 UTC) is already 02:30 on Jun 16 locally.
	loc := time.FixedZone("UTC+14", 14*3600)
	engine := newTestEngine(ts, Config{OpenMeteo: fake, Location: loc, StaleAfter: time.Hour})

	fc := engine.Compute()

	if got, want := fc.Days[0].Date, "2026-06-16"; got != want {
		t.Errorf("first day = %q, want %q (the station's local date)", got, want)
	}
	if got, want := fc.Days[0].DayOfWeek, "Tuesday"; got != want {
		t.Errorf("first day of week = %q, want %q", got, want)
	}
}

func TestEngineIncludesAuroraWhenKpAvailable(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, testNow, 72, 13.0, 80, 5.0)
	fake := &fakeOMClient{data: syntheticHourlyData(testNow, defaultPastDays, omForecastDaysBuffer)}
	kp := &fakeKpClient{points: []noaa.KpPoint{{Time: testNow.Add(-48 * time.Hour).Unix(), Kp: 7}}}
	engine := newTestEngine(ts, Config{OpenMeteo: fake, Kp: kp, Lat: fairbanksLat, Lon: fairbanksLon, StaleAfter: time.Hour})

	fc := engine.Compute()

	// Sunrise is 04:00 and sunset 20:00 UTC every day. Whatever the day, an
	// hour is dark exactly outside that span — in particular, midday on the
	// second and third days must not be marked dark.
	for di, day := range fc.Days {
		if day.Aurora == nil {
			t.Fatalf("Days[%d].Aurora = nil, want data (Kp available)", di)
		}
		for _, h := range day.Aurora.Hourly {
			hour := h.Time.UTC().Hour()
			wantDark := hour < 4 || hour >= 20
			if h.IsDark != wantDark {
				t.Errorf("Days[%d] %v: IsDark = %v, want %v", di, h.Time, h.IsDark, wantDark)
			}
			if !h.IsDark && h.ProbabilityPct != 0 {
				t.Errorf("Days[%d] %v: daylight probability = %.1f, want 0", di, h.Time, h.ProbabilityPct)
			}
		}
	}
}

func TestEngineOmitsAuroraWhenNOAAFails(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, testNow, 72, 13.0, 80, 5.0)
	fake := &fakeOMClient{data: syntheticHourlyData(testNow, defaultPastDays, omForecastDaysBuffer)}
	kp := &fakeKpClient{err: errors.New("simulated NOAA outage")}
	engine := newTestEngine(ts, Config{OpenMeteo: fake, Kp: kp, Lat: fairbanksLat, Lon: fairbanksLon, StaleAfter: time.Hour})

	fc := engine.Compute()

	if want := openmeteo.ModelName + "+local-bias-correction"; fc.Model != want {
		t.Fatalf("Model = %q, want %q (a NOAA outage must not demote the forecast)", fc.Model, want)
	}
	for i, day := range fc.Days {
		if day.Aurora != nil {
			t.Errorf("Days[%d].Aurora = %v, want nil", i, day.Aurora)
		}
		if day.Sunrise == nil || day.Sunset == nil {
			t.Errorf("Days[%d] sunrise/sunset missing, want them from Open-Meteo", i)
		}
	}
}

func TestEngineFallsBackToStationModelWhenECMWFFails(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, testNow, 72, 13.0, 80, 5.0)

	fake := &fakeOMClient{err: errors.New("simulated network failure")}
	engine := newTestEngine(ts, Config{OpenMeteo: fake, StaleAfter: time.Hour})

	fc := engine.Compute()
	const want = "station-holtwinters (open-meteo unavailable)"
	if fc.Model != want {
		t.Fatalf("Model = %q, want %q", fc.Model, want)
	}
}

func TestEngineFallsBackWhenForecastWindowHasMissingValues(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, testNow, 72, 13.0, 80, 5.0)

	data := syntheticHourlyData(testNow, defaultPastDays, omForecastDaysBuffer)
	// A null Open-Meteo value arrives as NaN; one inside the forecast window
	// must not be turned into a made-up 0 °C.
	startIdx := defaultPastDays*24 + 1
	data.TemperatureC[startIdx+10] = math.NaN()
	engine := newTestEngine(ts, Config{OpenMeteo: &fakeOMClient{data: data}, StaleAfter: time.Hour})

	fc := engine.Compute()
	if fc.Model != "station-holtwinters (open-meteo unavailable)" {
		t.Fatalf("Model = %q, want the station fallback", fc.Model)
	}
}

// A null in Open-Meteo's hindcast arrives as NaN. Where it lands on an hour the
// station also observed, an unguarded fit would turn it into a NaN bias for
// that hour-of-day and poison every forecast value in the bucket. The NaNs are
// placed on the station's overlap (not on hours it never observed, which the
// fit never reads), for all three corrected variables.
func TestEngineIgnoresMissingHindcastValuesInBiasFit(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	// The station reads 3 °C warmer, 5 % drier and 3 km/h windier than the
	// flat model (10 °C, 80 %, 5 km/h) at every hour of its last 3 days.
	stationHistory(ts, testNow, 72, 13.0, 75, 8.0)

	data := syntheticHourlyData(testNow, defaultPastDays, omForecastDaysBuffer)
	// data.Time starts at hour(testNow) - 7 days; the station's 72 hours begin
	// 3 days before testNow. Knock out the oldest of those three days: every
	// hour-of-day still keeps two valid samples (minBiasSamples).
	first := (defaultPastDays - 3) * 24
	for i := first; i < first+24; i++ {
		data.TemperatureC[i] = math.NaN()
		data.HumidityPct[i] = math.NaN()
		data.WindSpeedKmh[i] = math.NaN()
	}
	engine := newTestEngine(ts, Config{OpenMeteo: &fakeOMClient{data: data}, StaleAfter: time.Hour})

	fc := engine.Compute()

	if want := openmeteo.ModelName + "+local-bias-correction"; fc.Model != want {
		t.Fatalf("Model = %q, want %q", fc.Model, want)
	}
	// Written as an in-range check so that NaN fails it (NaN compares false).
	inRange := func(v, want float64) bool { return v >= want-0.5 && v <= want+0.5 }
	checked := 0
	for di, day := range fc.Days {
		for _, h := range day.Temperature.Hourly {
			if !inRange(h.Value, 13) {
				t.Errorf("Days[%d] temperature at %v = %v, want ~13 (bias learned from the remaining samples)", di, h.Time, h.Value)
			}
			checked++
		}
		for _, h := range day.Humidity.Hourly {
			if !inRange(h.Value, 75) {
				t.Errorf("Days[%d] humidity at %v = %v, want ~75", di, h.Time, h.Value)
			}
		}
		for _, h := range day.Wind.Hourly {
			if !inRange(h.SpeedKmh, 8) {
				t.Errorf("Days[%d] wind at %v = %v, want ~8", di, h.Time, h.SpeedKmh)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no forecast hours were checked")
	}
}

func TestEngineUsesStationModelWhenOpenMeteoNotConfigured(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, testNow, 72, 13.0, 80, 5.0)

	engine := newTestEngine(ts, Config{StaleAfter: time.Hour})
	fc := engine.Compute()

	const want = "station-holtwinters (open-meteo not configured)"
	if fc.Model != want {
		t.Fatalf("Model = %q, want %q", fc.Model, want)
	}
	for i, day := range fc.Days {
		if day.Sunrise != nil || day.Sunset != nil || day.Aurora != nil {
			t.Errorf("Days[%d] has sun/aurora data on the station-only path", i)
		}
	}
}

func TestStationModelStaysAlignedToClockHours(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	// The newest observation is 6 hours old (a quiet station): the model has
	// to look through that gap, so that values still land on the hours they
	// are labelled with.
	stationHistory(ts, testNow.Add(-6*time.Hour), 72, 13.0, 80, 5.0)
	engine := newTestEngine(ts, Config{StaleAfter: time.Hour})

	fc := engine.Compute()

	first := fc.Days[0].Temperature.Hourly[0]
	if want := firstForecastHour(testNow); !first.Time.Equal(want) {
		t.Errorf("first hour = %v, want %v", first.Time, want)
	}
	if first.Value < 12 || first.Value > 14 {
		t.Errorf("first hour temp = %.2f, want ~13 (flat history)", first.Value)
	}
	total := 0
	for _, d := range fc.Days {
		total += len(d.Temperature.Hourly)
	}
	if total < 49 || total > forecastHours {
		t.Errorf("forecast covers %d hours, want between 49 and %d", total, forecastHours)
	}
}
