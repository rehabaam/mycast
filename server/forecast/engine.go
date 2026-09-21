package forecast

import (
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/noaa"
	"github.com/rehabaam/mycast/openmeteo"
	"github.com/rehabaam/mycast/store"
)

const forecastHours = 72 // 3 days

// omPastDays is how much recent history to request from Open-Meteo for bias
// calibration — matched to the station's own default retention window.
// omForecastDaysBuffer requests extra days beyond the 3-day forecast so the
// window always covers a full 72h from "now", regardless of time of day.
const (
	omPastDays           = 7
	omForecastDaysBuffer = 4
)

// omClient is the subset of *openmeteo.Client the engine depends on, so
// tests can substitute a fake without a network call.
type omClient interface {
	Fetch(pastDays, forecastDays int) (*openmeteo.HourlyData, error)
}

// kpClient is the subset of *noaa.Client the engine depends on, so tests can
// substitute a fake without a network call.
type kpClient interface {
	FetchKpForecast() ([]noaa.KpPoint, error)
}

// Engine builds the 3-day forecast from the time series store, preferring an
// NWP forecast (Open-Meteo/ECMWF) with a local bias correction, and falling
// back to the pure station-based model if Open-Meteo is unavailable.
type Engine struct {
	ts         *store.TimeSeries
	mu         sync.RWMutex
	cached     *Forecast
	station    string
	om         omClient
	kp         kpClient
	geomagLat  float64
	staleAfter time.Duration
}

// NewEngine creates an Engine. om may be nil, in which case the engine
// always uses the station-only model. kp may also be nil, in which case
// aurora data is simply omitted (it's a bonus on top of the ECMWF path, not
// required for it). lat/lon are the station's coordinates, used to derive
// its geomagnetic latitude (see aurora.go) for aurora visibility — only
// meaningful when kp is non-nil, and may be left zero otherwise. staleAfter
// is how old a cached forecast can get before Fresh() forces a synchronous
// recompute and flags the result as stale — normally a small multiple of
// the scheduler's fetch interval, so a single missed tick doesn't
// immediately trip it.
func NewEngine(ts *store.TimeSeries, stationID string, om omClient, kp kpClient, lat, lon float64, staleAfter time.Duration) *Engine {
	return &Engine{ts: ts, station: stationID, om: om, kp: kp, geomagLat: geomagneticLatitude(lat, lon), staleAfter: staleAfter}
}

// Compute rebuilds the 3-day forecast from the current time series. Safe to
// call concurrently.
func (e *Engine) Compute() *Forecast {
	obs := e.ts.All()
	if len(obs) == 0 {
		return &Forecast{GeneratedAt: time.Now().UTC(), StationID: e.station}
	}

	var fc *Forecast
	if e.om != nil {
		var err error
		fc, err = e.computeFromECMWF(obs)
		if err != nil {
			log.Printf("forecast: open-meteo unavailable, falling back to station model: %v", err)
		}
	}
	if fc == nil {
		fc = e.computeFromStation(obs, e.om != nil)
	}

	e.mu.Lock()
	e.cached = fc
	e.mu.Unlock()

	return fc
}

// computeFromECMWF fetches an NWP forecast for the station's coordinates,
// learns an hour-of-day bias correction from the overlap between the
// model's recent hindcast and the station's own observations (Model Output
// Statistics), and applies it to the forecast window.
func (e *Engine) computeFromECMWF(obs []netatmo.Observation) (*Forecast, error) {
	data, err := e.om.Fetch(omPastDays, omForecastDaysBuffer)
	if err != nil {
		return nil, err
	}
	n := len(data.Time)
	if len(data.TemperatureC) != n || len(data.HumidityPct) != n || len(data.WindSpeedKmh) != n ||
		len(data.WindDirDeg) != n || len(data.PrecipMM) != n || len(data.PrecipProbPct) != n ||
		len(data.WeatherCode) != n || len(data.CloudCoverPct) != n {
		return nil, fmt.Errorf("open-meteo: mismatched hourly array lengths in response")
	}
	dn := len(data.Daily.Time)
	if len(data.Daily.Sunrise) != dn || len(data.Daily.Sunset) != dn {
		return nil, fmt.Errorf("open-meteo: mismatched daily array lengths in response")
	}

	actualTemp := map[int64]float64{}
	actualHumid := map[int64]float64{}
	actualSpeed := map[int64]float64{}
	for _, o := range obs {
		hourTS := (o.Timestamp / 3600) * 3600
		actualTemp[hourTS] = o.Temperature
		actualHumid[hourTS] = o.Humidity
		actualSpeed[hourTS] = o.WindSpeed
	}

	startTS := time.Now().UTC().Truncate(time.Hour).Add(time.Hour).Unix()

	predTemp := map[int64]float64{}
	predHumid := map[int64]float64{}
	predSpeed := map[int64]float64{}
	startIdx := -1
	for i, t := range data.Time {
		if t < startTS {
			predTemp[t] = data.TemperatureC[i]
			predHumid[t] = data.HumidityPct[i]
			predSpeed[t] = data.WindSpeedKmh[i]
		} else if startIdx == -1 {
			startIdx = i
		}
	}
	if startIdx == -1 || startIdx+forecastHours > len(data.Time) {
		return nil, fmt.Errorf("open-meteo: response does not cover the full %dh forecast window", forecastHours)
	}

	tempBias := fitHourlyBias(actualTemp, predTemp)
	humidBias := fitHourlyBias(actualHumid, predHumid)
	speedBias := fitHourlyBias(actualSpeed, predSpeed)

	fcTS := data.Time[startIdx : startIdx+forecastHours]
	temps := tempBias.apply(fcTS, data.TemperatureC[startIdx:startIdx+forecastHours])
	humid := humidBias.apply(fcTS, data.HumidityPct[startIdx:startIdx+forecastHours])
	speeds := speedBias.apply(fcTS, data.WindSpeedKmh[startIdx:startIdx+forecastHours])
	// Wind direction is circular — left uncorrected for now rather than
	// applying a linear bias that would be meaningless across the 0/360 wrap.
	angles := append([]float64(nil), data.WindDirDeg[startIdx:startIdx+forecastHours]...)

	clampSlice(temps, -50, 60)
	clampSlice(humid, 0, 100)
	clampSlice(speeds, 0, 300)

	codes := make([]int, forecastHours)
	for i, c := range data.WeatherCode[startIdx : startIdx+forecastHours] {
		codes[i] = int(c)
	}
	cloudCover := append([]float64(nil), data.CloudCoverPct[startIdx:startIdx+forecastHours]...)

	// Precipitation: use ECMWF's own amount + probability rather than the
	// local persistence+climatology model. Backtested against this
	// station's real rain history at a genuine 72h lead, the local model
	// never once flagged an actual rain hour (Brier score identical to a
	// constant climatological guess), while ECMWF caught several. No local
	// bias correction is applied here — precipitation's zero-inflated,
	// skewed distribution makes an additive MOS-style correction risky with
	// this little local rain history to calibrate against.
	amounts := append([]float64(nil), data.PrecipMM[startIdx:startIdx+forecastHours]...)
	clampSlice(amounts, 0, 200)
	probs := make([]float64, forecastHours)
	for i, p := range data.PrecipProbPct[startIdx : startIdx+forecastHours] {
		probs[i] = clamp(p/100.0, 0, 1)
	}
	precip := precipResult{amounts: amounts, probabilities: probs}

	sunriseByDate := map[string]time.Time{}
	sunsetByDate := map[string]time.Time{}
	for i, t := range data.Daily.Time {
		dateStr := time.Unix(t, 0).UTC().Format("2006-01-02")
		sunriseByDate[dateStr] = time.Unix(data.Daily.Sunrise[i], 0).UTC()
		sunsetByDate[dateStr] = time.Unix(data.Daily.Sunset[i], 0).UTC()
	}

	// Aurora is a bonus on top of the ECMWF path, not required for it — if
	// NOAA is unreachable, kpHourly stays nil and buildDays simply omits
	// aurora data rather than failing the whole forecast.
	var kpHourly []float64
	if e.kp != nil {
		if kpPoints, err := e.kp.FetchKpForecast(); err != nil {
			log.Printf("forecast: noaa kp forecast unavailable, omitting aurora data: %v", err)
		} else {
			kpHourly = make([]float64, forecastHours)
			for i, t := range fcTS {
				kpHourly[i] = kpForHour(kpPoints, t)
			}
		}
	}

	return &Forecast{
		GeneratedAt: time.Now().UTC(),
		StationID:   e.station,
		Model:       openmeteo.ModelName + "+local-bias-correction",
		Days:        buildDays(temps, humid, speeds, angles, codes, sunriseByDate, sunsetByDate, cloudCover, kpHourly, e.geomagLat, precip),
	}, nil
}

// kpForHour returns the Kp value from the latest bucket at or before t
// (NOAA's timestamps are 3-hour-bucket start times, sorted ascending).
func kpForHour(points []noaa.KpPoint, t int64) float64 {
	if len(points) == 0 {
		return 0
	}
	best := points[0].Kp
	for _, p := range points {
		if p.Time > t {
			break
		}
		best = p.Kp
	}
	return best
}

// computeFromStation runs the pure station-based model (deterministic
// damped-trend Holt-Winters). omConfigured distinguishes "Open-Meteo was
// never configured" from "Open-Meteo failed" in the reported model name.
func (e *Engine) computeFromStation(obs []netatmo.Observation, omConfigured bool) *Forecast {
	n := len(obs)
	temps := make([]float64, n)
	humid := make([]float64, n)
	speeds := make([]float64, n)
	sinAng := make([]float64, n)
	cosAng := make([]float64, n)
	rains := make([]float64, n)

	for i, o := range obs {
		temps[i] = o.Temperature
		humid[i] = o.Humidity
		speeds[i] = o.WindSpeed
		rad := o.WindAngle * math.Pi / 180.0
		sinAng[i] = math.Sin(rad)
		cosAng[i] = math.Cos(rad)
		rains[i] = o.Rain
	}

	type results struct {
		temps  []float64
		humid  []float64
		speeds []float64
		sinAng []float64
		cosAng []float64
		precip precipResult
	}

	var wg sync.WaitGroup
	res := results{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.temps = holtWintersForecast(temps, hwSeason, forecastHours)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.humid = holtWintersForecast(humid, hwSeason, forecastHours)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.speeds = holtWintersForecast(speeds, hwSeason, forecastHours)
	}()

	// Wind direction: decompose to (sin, cos), forecast each, reconstruct.
	wg.Add(1)
	go func() {
		defer wg.Done()
		res.sinAng = holtWintersForecast(sinAng, hwSeason, forecastHours)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.cosAng = holtWintersForecast(cosAng, hwSeason, forecastHours)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.precip = precipForecast(rains, forecastHours)
	}()

	wg.Wait()

	// Reconstruct wind angles from sin/cos forecasts.
	fcAngles := make([]float64, forecastHours)
	for i := range fcAngles {
		deg := math.Atan2(res.sinAng[i], res.cosAng[i]) * 180.0 / math.Pi
		if deg < 0 {
			deg += 360
		}
		fcAngles[i] = deg
	}

	// Clamp forecasts to physical limits.
	clampSlice(res.temps, -50, 60)
	clampSlice(res.humid, 0, 100)
	clampSlice(res.speeds, 0, 300)

	model := "station-holtwinters (open-meteo not configured)"
	if omConfigured {
		model = "station-holtwinters (open-meteo unavailable)"
	}

	return &Forecast{
		GeneratedAt: time.Now().UTC(),
		StationID:   e.station,
		Model:       model,
		// No weather-code, sunrise/sunset, cloud cover, or Kp source on this
		// path — buildDays degrades condition summaries to "Unknown" and
		// omits sunrise/sunset/aurora when these are nil.
		Days: buildDays(res.temps, res.humid, res.speeds, fcAngles, nil, nil, nil, nil, nil, 0, res.precip),
	}
}

// Latest returns the most recently computed forecast, or nil, without
// checking its age or triggering a recompute.
func (e *Engine) Latest() *Forecast {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cached
}

// Fresh returns a forecast that is never older than staleAfter, forcing a
// synchronous recompute first if the cache has gone stale (e.g. because the
// background scheduler missed ticks or the process was paused). The
// returned forecast's Stale field reports whether it's still older than
// staleAfter even after that attempt — e.g. if the time series has no data
// at all yet, or was itself never refreshed.
func (e *Engine) Fresh() *Forecast {
	e.mu.RLock()
	cached := e.cached
	e.mu.RUnlock()

	if cached == nil || time.Since(cached.GeneratedAt) > e.staleAfter {
		cached = e.Compute()
	}

	result := *cached
	result.Stale = time.Since(result.GeneratedAt) > e.staleAfter
	return &result
}

// buildDays assembles the 3-day response from per-hour forecast arrays.
// codes may be nil (station-only path has no weather-code source), in which
// case condition summaries degrade to "Unknown" rather than being omitted.
// sunriseByDate/sunsetByDate (keyed by "2006-01-02") may also be nil, in
// which case Sunrise/Sunset are left nil (omitted from JSON). cloudCover and
// kpHourly must both be non-nil for aurora data to be computed at all
// (station-only path has neither) — geomagLat is only meaningful then.
func buildDays(temps, humid, speeds, angles []float64, codes []int, sunriseByDate, sunsetByDate map[string]time.Time, cloudCover, kpHourly []float64, geomagLat float64, precip precipResult) []DayForecast {
	now := time.Now().UTC()
	start := now.Truncate(time.Hour).Add(time.Hour)

	codeAt := func(idx int) int {
		if idx < len(codes) {
			return codes[idx]
		}
		return -1 // no code available -> "Unknown"
	}

	auroraAvailable := cloudCover != nil && kpHourly != nil
	var reqKp float64
	if auroraAvailable {
		reqKp = requiredKp(geomagLat)
	}

	days := make([]DayForecast, 3)
	for d := 0; d < 3; d++ {
		date := start.Add(time.Duration(d*24) * time.Hour)
		dateStr := date.Format("2006-01-02")

		var sunrise, sunset *time.Time
		if sr, ok := sunriseByDate[dateStr]; ok {
			sunrise = &sr
		}
		if ss, ok := sunsetByDate[dateStr]; ok {
			sunset = &ss
		}

		var tH, hH, aH []HourlyPoint
		var wH []WindHourly
		var pH []PrecipHourly
		var cH []ConditionHourly
		var auH []AuroraHourly

		var tSum, hSum, sSum, sMax, aSum, auroraMax float64
		var dirAngles []float64
		middayCode := -1

		for h := 0; h < 24; h++ {
			idx := d*24 + h
			if idx >= forecastHours {
				break
			}
			t := start.Add(time.Duration(idx) * time.Hour)

			tH = append(tH, HourlyPoint{Time: t, Value: round2(temps[idx])})
			hH = append(hH, HourlyPoint{Time: t, Value: round2(humid[idx])})

			apparent := apparentTemperatureC(temps[idx], humid[idx], speeds[idx])
			aH = append(aH, HourlyPoint{Time: t, Value: round2(apparent)})
			aSum += apparent

			code := codeAt(idx)
			cH = append(cH, ConditionHourly{Time: t, Code: code, Summary: conditionSummary(code)})
			if h == 12 {
				middayCode = code
			}

			if auroraAvailable {
				isDark := sunrise != nil && sunset != nil && (t.Before(*sunrise) || t.After(*sunset))
				prob := auroraProbabilityPct(kpHourly[idx], reqKp, cloudCover[idx], isDark)
				auH = append(auH, AuroraHourly{
					Time:           t,
					Kp:             round2(kpHourly[idx]),
					RequiredKp:     round2(reqKp),
					CloudCoverPct:  round2(cloudCover[idx]),
					IsDark:         isDark,
					ProbabilityPct: round2(prob),
				})
				if prob > auroraMax {
					auroraMax = prob
				}
			}

			spd := round2(speeds[idx])
			dir := round2(angles[idx])
			wH = append(wH, WindHourly{
				Time:         t,
				SpeedKmh:     spd,
				DirectionDeg: dir,
				Cardinal:     degreeToCardinal(dir),
			})
			pH = append(pH, PrecipHourly{
				Time:        t,
				AmountMM:    round2(precip.amounts[idx]),
				Probability: round2(precip.probabilities[idx]),
			})

			tSum += temps[idx]
			hSum += humid[idx]
			sSum += speeds[idx]
			if speeds[idx] > sMax {
				sMax = speeds[idx]
			}
			dirAngles = append(dirAngles, angles[idx])
		}

		cnt := float64(len(tH))
		avgDir := circularMean(dirAngles)
		daySlice := func(s []float64) []float64 {
			lo, hi := d*24, (d+1)*24
			if hi > len(s) {
				hi = len(s)
			}
			return s[lo:hi]
		}

		// If local midday fell outside this day's slice (shouldn't happen
		// with a 24h day, but guards the general case), fall back to the
		// first available hour's condition.
		if middayCode == -1 && len(cH) > 0 {
			middayCode = cH[0].Code
		}

		var aurora *AuroraDay
		if auroraAvailable {
			aurora = &AuroraDay{MaxProbabilityPct: round2(auroraMax), Hourly: auH}
		}

		days[d] = DayForecast{
			Date:      dateStr,
			DayOfWeek: date.Format("Monday"),
			Sunrise:   sunrise,
			Sunset:    sunset,
			Aurora:    aurora,
			Condition: ConditionDay{
				Summary: conditionSummary(middayCode),
				Hourly:  cH,
			},
			Temperature: TemperatureDay{
				MinC:           round2(sliceMin(daySlice(temps))),
				MaxC:           round2(sliceMax(daySlice(temps))),
				AvgC:           round2(tSum / cnt),
				Hourly:         tH,
				ApparentAvgC:   round2(aSum / cnt),
				ApparentHourly: aH,
			},
			Humidity: HumidityDay{
				MinPct: round2(sliceMin(daySlice(humid))),
				MaxPct: round2(sliceMax(daySlice(humid))),
				AvgPct: round2(hSum / cnt),
				Hourly: hH,
			},
			Wind: WindDay{
				AvgSpeedKmh: round2(sSum / cnt),
				MaxSpeedKmh: round2(sMax),
				AvgDirDeg:   round2(avgDir),
				Cardinal:    degreeToCardinal(avgDir),
				Hourly:      wH,
			},
			Precip: PrecipDay{
				TotalMM:     round2(sliceSum(daySlice(precip.amounts))),
				Probability: round2(sliceMax(daySlice(precip.probabilities))),
				Hourly:      pH,
			},
		}
	}
	return days
}

func clampSlice(s []float64, lo, hi float64) {
	for i, v := range s {
		if v < lo {
			s[i] = lo
		} else if v > hi {
			s[i] = hi
		}
	}
}

func sliceMin(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	m := s[0]
	for _, v := range s[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func sliceMax(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	m := s[0]
	for _, v := range s[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func sliceSum(s []float64) float64 {
	var t float64
	for _, v := range s {
		t += v
	}
	return t
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
