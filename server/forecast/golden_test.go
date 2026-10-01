package forecast

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rehabaam/mycast/noaa"
	"github.com/rehabaam/mycast/store"
)

// The golden files are the server's real /forecast output for each path the
// engine can take. They live in the Swift app's test bundle, so the app's
// decoding tests read exactly what these tests pin: if the server's JSON
// changes, this fails until the fixtures are regenerated (and then the Swift
// tests show whether the app still copes).
//
//	go test ./forecast -run Golden -update
var update = flag.Bool("update", false, "rewrite the golden fixtures shared with the Swift app's tests")

const fixtureDir = "../../app/MyCastTests/Fixtures"

func checkGolden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join(fixtureDir, name)
	if *update {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\nregenerate with: go test ./forecast -run Golden -update", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s no longer matches the server's output; regenerate with: go test ./forecast -run Golden -update", name)
	}
}

// goldenWeather fills in varied (but deterministic) hourly data, so the
// fixtures exercise real-looking numbers rather than flat lines.
func goldenWeather(t *testing.T) (*store.TimeSeries, *fakeOMClient) {
	t.Helper()
	ts := store.NewTimeSeries(24 * 10)
	hour := testNow.Truncate(time.Hour)
	for h := 96; h >= 1; h-- {
		at := hour.Add(-time.Duration(h) * time.Hour)
		phase := 2 * math.Pi * float64(at.Hour()) / 24
		ts.Append(obsAtTime(at, 12+4*math.Sin(phase-2), 70+15*math.Cos(phase), 6+3*math.Sin(phase)))
	}

	data := syntheticHourlyData(testNow, defaultPastDays, omForecastDaysBuffer)
	for i, ti := range data.Time {
		at := time.Unix(ti, 0).UTC()
		phase := 2 * math.Pi * float64(at.Hour()) / 24
		day := float64(i / 24 % 3)
		data.TemperatureC[i] = 10 + 4*math.Sin(phase-2) + day
		data.HumidityPct[i] = 72 + 15*math.Cos(phase)
		data.WindSpeedKmh[i] = 5 + 3*math.Sin(phase)
		data.WindDirDeg[i] = math.Mod(180+float64(i)*7, 360)
		data.CloudCoverPct[i] = math.Mod(float64(i)*13, 100)
		if i%24 >= 12 && i%24 < 16 {
			data.WeatherCode[i] = 61
			data.PrecipMM[i] = 0.4
			data.PrecipProbPct[i] = 60
		}
	}
	return ts, &fakeOMClient{data: data}
}

func goldenConfig() Config {
	return Config{
		StationID:  "70:ee:50:00:00:01",
		Lat:        fairbanksLat,
		Lon:        fairbanksLon,
		Location:   time.FixedZone("EEST", 3*3600),
		StaleAfter: time.Hour,
	}
}

func TestGoldenForecastFull(t *testing.T) {
	ts, om := goldenWeather(t)
	cfg := goldenConfig()
	cfg.OpenMeteo = om
	cfg.Kp = &fakeKpClient{points: []noaa.KpPoint{{Time: testNow.Add(-24 * time.Hour).Unix(), Kp: 5}}}

	fc := newTestEngine(ts, cfg).Compute()

	if fc.Days[0].Aurora == nil || fc.Days[0].Sunrise == nil {
		t.Fatal("fixture scenario should carry sun and aurora data")
	}
	checkGolden(t, "forecast_full.json", fc)
}

func TestGoldenForecastWithoutAurora(t *testing.T) {
	ts, om := goldenWeather(t)
	cfg := goldenConfig()
	cfg.OpenMeteo = om
	cfg.Kp = &fakeKpClient{err: errors.New("noaa down")}

	fc := newTestEngine(ts, cfg).Compute()

	if fc.Days[0].Aurora != nil || fc.Days[0].Sunrise == nil {
		t.Fatal("fixture scenario should have sun data but no aurora")
	}
	checkGolden(t, "forecast_no_aurora.json", fc)
}

func TestGoldenForecastStationOnlyFallback(t *testing.T) {
	ts, om := goldenWeather(t)
	cfg := goldenConfig()
	cfg.OpenMeteo = &fakeOMClient{err: errors.New("open-meteo down")}
	_ = om

	fc := newTestEngine(ts, cfg).Compute()

	if fc.Days[0].Aurora != nil || fc.Days[0].Sunrise != nil || fc.Days[0].Sunset != nil {
		t.Fatal("fixture scenario should have neither sun nor aurora data")
	}
	checkGolden(t, "forecast_station_only.json", fc)
}
