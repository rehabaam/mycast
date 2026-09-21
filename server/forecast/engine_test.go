package forecast

import (
	"errors"
	"testing"
	"time"

	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/noaa"
	"github.com/rehabaam/mycast/openmeteo"
	"github.com/rehabaam/mycast/store"
)

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
// to real "now" so it aligns with the engine's own time window logic.
func syntheticHourlyData(pastDays, forecastDays int) *openmeteo.HourlyData {
	now := time.Now().UTC().Truncate(time.Hour)
	start := now.Add(-time.Duration(pastDays) * 24 * time.Hour)
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
	return d
}

// stationHistory appends `hours` of constant-valued observations ending 1h
// ago, aligned to whole-hour boundaries like real station data.
func stationHistory(ts *store.TimeSeries, hours int, temp, humidity, windSpeed float64) {
	now := time.Now().UTC()
	for h := hours; h >= 1; h-- {
		obsTime := now.Add(-time.Duration(h) * time.Hour).Truncate(time.Hour)
		ts.Append(netatmo.Observation{
			Timestamp:   obsTime.Unix(),
			Temperature: temp,
			Humidity:    humidity,
			WindSpeed:   windSpeed,
			WindAngle:   180,
		})
	}
}

func TestEngineUsesECMWFWithBiasCorrectionWhenAvailable(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	// Station consistently reads 3°C warmer than the "model" (a flat 10°C)
	// at every hour over the past 3 days — plenty of samples per hour-of-day
	// bucket for the bias fit to trust.
	stationHistory(ts, 72, 13.0, 80, 5.0)

	fake := &fakeOMClient{data: syntheticHourlyData(omPastDays, omForecastDaysBuffer)}
	engine := NewEngine(ts, "test-station", fake, nil, 64.84, -147.72, time.Hour)

	fc := engine.Compute()
	wantModel := openmeteo.ModelName + "+local-bias-correction"
	if fc.Model != wantModel {
		t.Fatalf("Model = %q, want %q", fc.Model, wantModel)
	}

	// The bias-corrected forecast should track ~13°C (10 + learned +3°
	// bias), not the raw uncorrected model output of 10°C.
	firstHourTemp := fc.Days[0].Temperature.Hourly[0].Value
	if firstHourTemp < 12.5 || firstHourTemp > 13.5 {
		t.Errorf("first hour temp = %.2f, want ~13.0 (bias-corrected)", firstHourTemp)
	}
}

func TestEngineFallsBackToStationModelWhenECMWFFails(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, 72, 13.0, 80, 5.0)

	fake := &fakeOMClient{err: errors.New("simulated network failure")}
	engine := NewEngine(ts, "test-station", fake, nil, 64.84, -147.72, time.Hour)

	fc := engine.Compute()
	const want = "station-holtwinters (open-meteo unavailable)"
	if fc.Model != want {
		t.Fatalf("Model = %q, want %q", fc.Model, want)
	}
}

func TestEngineUsesStationModelWhenOpenMeteoNotConfigured(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	stationHistory(ts, 72, 13.0, 80, 5.0)

	engine := NewEngine(ts, "test-station", nil, nil, 0, 0, time.Hour)
	fc := engine.Compute()

	const want = "station-holtwinters (open-meteo not configured)"
	if fc.Model != want {
		t.Fatalf("Model = %q, want %q", fc.Model, want)
	}
}
