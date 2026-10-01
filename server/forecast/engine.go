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

const (
	forecastHours = 72 // modelled horizon
	forecastDays  = 3  // local calendar days reported

	// defaultPastDays is how much recent history to request from Open-Meteo
	// for bias calibration when Config.PastDays is unset. Open-Meteo accepts
	// at most maxPastDays.
	defaultPastDays = 7
	maxPastDays     = 92

	// omForecastDaysBuffer requests extra days beyond the 3-day forecast so
	// the window always covers a full 72h from "now", regardless of time of
	// day.
	omForecastDaysBuffer = 4
)

// OpenMeteoSource supplies the NWP forecast. *openmeteo.Client implements it;
// tests substitute a fake.
type OpenMeteoSource interface {
	Fetch(pastDays, forecastDays int) (*openmeteo.HourlyData, error)
}

// KpSource supplies NOAA's Kp index forecast. *noaa.Client implements it;
// tests substitute a fake.
type KpSource interface {
	FetchKpForecast() ([]noaa.KpPoint, error)
}

// Config holds an Engine's collaborators and tunables.
type Config struct {
	// StationID is echoed in responses.
	StationID string

	// OpenMeteo is the NWP source. If nil the engine always uses the
	// station-only model.
	OpenMeteo OpenMeteoSource

	// Kp is the aurora source. If nil, aurora data is omitted — it is a
	// bonus on top of the ECMWF path, not required for it.
	Kp KpSource

	// Lat/Lon are the station's coordinates, used to derive its geomagnetic
	// latitude (see aurora.go). Only meaningful when Kp is set.
	Lat, Lon float64

	// Location decides where one forecast "day" ends. It should be the
	// station's local timezone; nil means UTC.
	Location *time.Location

	// StaleAfter is how long the engine tolerates silence from the station
	// before flagging forecasts as stale, and how old a cached forecast may
	// get before Fresh() recomputes it. Normally a small multiple of the
	// scheduler's fetch interval, so a single missed tick doesn't trip it.
	StaleAfter time.Duration

	// PastDays is how many days of Open-Meteo hindcast to compare against the
	// station's history when learning the bias correction. It should match
	// the station history retained. Zero means 7.
	PastDays int
}

// Engine builds the 3-day forecast from the time series store, preferring an
// NWP forecast (Open-Meteo/ECMWF) with a local bias correction, and falling
// back to the pure station-based model if Open-Meteo is unavailable.
type Engine struct {
	ts        *store.TimeSeries
	cfg       Config
	loc       *time.Location
	geomagLat float64
	now       func() time.Time // replaceable in tests

	// computeMu serialises recomputes, so concurrent stale requests share one
	// computation and a slow one can never overwrite a newer result.
	computeMu sync.Mutex

	mu     sync.RWMutex // guards cached
	cached *Forecast
}

// NewEngine creates an Engine over the given time series.
func NewEngine(ts *store.TimeSeries, cfg Config) *Engine {
	if cfg.PastDays <= 0 {
		cfg.PastDays = defaultPastDays
	}
	if cfg.PastDays > maxPastDays {
		cfg.PastDays = maxPastDays
	}
	loc := cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	return &Engine{
		ts:        ts,
		cfg:       cfg,
		loc:       loc,
		geomagLat: geomagneticLatitude(cfg.Lat, cfg.Lon),
		now:       time.Now,
	}
}

// generatedAt is the timestamp stamped on a forecast: second precision, since
// sub-second noise is meaningless to consumers.
func (e *Engine) generatedAt() time.Time {
	return e.now().UTC().Truncate(time.Second)
}

// firstForecastHour is the first hour a forecast covers: the next whole hour.
func firstForecastHour(now time.Time) time.Time {
	return now.UTC().Truncate(time.Hour).Add(time.Hour)
}

// Compute rebuilds the forecast from the current time series. Safe to call
// concurrently.
func (e *Engine) Compute() *Forecast {
	e.computeMu.Lock()
	defer e.computeMu.Unlock()
	return e.computeLocked()
}

// computeLocked does the work of Compute; the caller holds computeMu.
func (e *Engine) computeLocked() *Forecast {
	obs := e.ts.All()
	if len(obs) == 0 {
		return &Forecast{GeneratedAt: e.generatedAt(), StationID: e.cfg.StationID, Days: []DayForecast{}}
	}

	var fc *Forecast
	if e.cfg.OpenMeteo != nil {
		var err error
		fc, err = e.computeFromECMWF(obs)
		if err != nil {
			log.Printf("forecast: open-meteo unavailable, falling back to station model: %v", err)
		}
	}
	if fc == nil {
		fc = e.computeFromStation(obs, e.cfg.OpenMeteo != nil)
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
	now := e.now()
	data, err := e.cfg.OpenMeteo.Fetch(e.cfg.PastDays, omForecastDaysBuffer)
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
		hourTS := (o.Timestamp / secondsPerHour) * secondsPerHour
		actualTemp[hourTS] = o.Temperature
		actualHumid[hourTS] = o.Humidity
		actualSpeed[hourTS] = o.WindSpeed
	}

	startTS := firstForecastHour(now).Unix()

	// Hours before the window are hindcast; hours with a missing (NaN) model
	// value are left out of the bias fit rather than polluting it.
	predTemp := map[int64]float64{}
	predHumid := map[int64]float64{}
	predSpeed := map[int64]float64{}
	startIdx := -1
	for i, t := range data.Time {
		if t < startTS {
			if isFinite(data.TemperatureC[i]) {
				predTemp[t] = data.TemperatureC[i]
			}
			if isFinite(data.HumidityPct[i]) {
				predHumid[t] = data.HumidityPct[i]
			}
			if isFinite(data.WindSpeedKmh[i]) {
				predSpeed[t] = data.WindSpeedKmh[i]
			}
		} else if startIdx == -1 {
			startIdx = i
		}
	}
	if startIdx == -1 || startIdx+forecastHours > len(data.Time) {
		return nil, fmt.Errorf("open-meteo: response does not cover the full %dh forecast window", forecastHours)
	}
	end := startIdx + forecastHours

	// The forecast window itself must be complete: a missing value there
	// can't be papered over without inventing weather.
	for _, f := range []struct {
		name string
		v    []float64
	}{
		{"temperature", data.TemperatureC},
		{"humidity", data.HumidityPct},
		{"wind speed", data.WindSpeedKmh},
		{"wind direction", data.WindDirDeg},
		{"precipitation", data.PrecipMM},
		{"weather code", data.WeatherCode},
		{"cloud cover", data.CloudCoverPct},
	} {
		if hasNonFinite(f.v[startIdx:end]) {
			return nil, fmt.Errorf("open-meteo: missing %s values in the forecast window", f.name)
		}
	}

	tempBias := fitHourlyBias(actualTemp, predTemp)
	humidBias := fitHourlyBias(actualHumid, predHumid)
	speedBias := fitHourlyBias(actualSpeed, predSpeed)

	fcTS := data.Time[startIdx:end]
	temps := tempBias.apply(fcTS, data.TemperatureC[startIdx:end])
	humid := humidBias.apply(fcTS, data.HumidityPct[startIdx:end])
	speeds := speedBias.apply(fcTS, data.WindSpeedKmh[startIdx:end])
	// Wind direction is circular — left uncorrected for now rather than
	// applying a linear bias that would be meaningless across the 0/360 wrap.
	angles := append([]float64(nil), data.WindDirDeg[startIdx:end]...)

	clampSlice(temps, -50, 60)
	clampSlice(humid, 0, 100)
	clampSlice(speeds, 0, 300)

	codes := make([]int, forecastHours)
	for i, c := range data.WeatherCode[startIdx:end] {
		codes[i] = int(c)
	}
	cloudCover := append([]float64(nil), data.CloudCoverPct[startIdx:end]...)

	// Precipitation: use ECMWF's own amount + probability rather than the
	// local persistence+climatology model. Backtested against this
	// station's real rain history at a genuine 72h lead, the local model
	// never once flagged an actual rain hour (Brier score identical to a
	// constant climatological guess), while ECMWF caught several. No local
	// bias correction is applied here — precipitation's zero-inflated,
	// skewed distribution makes an additive MOS-style correction risky with
	// this little local rain history to calibrate against. A missing
	// probability is treated as 0 (Open-Meteo derives it, and doesn't always
	// have one).
	amounts := append([]float64(nil), data.PrecipMM[startIdx:end]...)
	clampSlice(amounts, 0, 200)
	probs := make([]float64, forecastHours)
	for i, p := range data.PrecipProbPct[startIdx:end] {
		if !isFinite(p) {
			continue
		}
		probs[i] = clamp(p/100.0, 0, 1)
	}
	precip := precipResult{amounts: amounts, probabilities: probs}

	sunrises := make([]time.Time, len(data.Daily.Sunrise))
	for i, t := range data.Daily.Sunrise {
		sunrises[i] = time.Unix(t, 0).UTC()
	}
	sunsets := make([]time.Time, len(data.Daily.Sunset))
	for i, t := range data.Daily.Sunset {
		sunsets[i] = time.Unix(t, 0).UTC()
	}

	// Aurora is a bonus on top of the ECMWF path, not required for it — if
	// NOAA is unreachable, kpHourly stays nil and buildDays simply omits
	// aurora data rather than failing the whole forecast.
	var kpHourly []float64
	if e.cfg.Kp != nil {
		if kpPoints, err := e.cfg.Kp.FetchKpForecast(); err != nil {
			log.Printf("forecast: noaa kp forecast unavailable, omitting aurora data: %v", err)
		} else {
			kpHourly = make([]float64, forecastHours)
			for i, t := range fcTS {
				kpHourly[i] = kpForHour(kpPoints, t)
			}
		}
	}

	return &Forecast{
		GeneratedAt: e.generatedAt(),
		StationID:   e.cfg.StationID,
		Model:       openmeteo.ModelName + "+local-bias-correction",
		Days: buildDays(dayInput{
			now:        now,
			loc:        e.loc,
			temps:      temps,
			humid:      humid,
			speeds:     speeds,
			angles:     angles,
			codes:      codes,
			sunrises:   sunrises,
			sunsets:    sunsets,
			cloudCover: cloudCover,
			kp:         kpHourly,
			geomagLat:  e.geomagLat,
			precip:     precip,
		}),
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
	now := e.now()

	// The seasonal models index by slice position, so give them a series
	// with exactly one sample per clock hour.
	grid := hourlyGrid(obs)
	n := len(grid)
	lastHour := time.Unix(grid[n-1].Timestamp, 0).UTC()

	// If the newest data is older than the previous hour, the first
	// forecast step is not the first hour we report: model through the gap
	// and discard it, so every value lands on the hour it is labelled with.
	skip := int(firstForecastHour(now).Sub(lastHour)/time.Hour) - 1
	if skip < 0 {
		skip = 0
	}
	horizon := skip + forecastHours

	temps := make([]float64, n)
	humid := make([]float64, n)
	speeds := make([]float64, n)
	sinAng := make([]float64, n)
	cosAng := make([]float64, n)
	rains := make([]float64, n)

	for i, o := range grid {
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
		res.temps = holtWintersForecast(temps, hwSeason, horizon)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.humid = holtWintersForecast(humid, hwSeason, horizon)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.speeds = holtWintersForecast(speeds, hwSeason, horizon)
	}()

	// Wind direction: decompose to (sin, cos), forecast each, reconstruct.
	wg.Add(1)
	go func() {
		defer wg.Done()
		res.sinAng = holtWintersForecast(sinAng, hwSeason, horizon)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.cosAng = holtWintersForecast(cosAng, hwSeason, horizon)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		res.precip = precipForecast(rains, lastHour.Hour(), horizon)
	}()

	wg.Wait()

	// Reconstruct wind angles from sin/cos forecasts.
	fcAngles := make([]float64, forecastHours)
	for i := range fcAngles {
		deg := math.Atan2(res.sinAng[skip+i], res.cosAng[skip+i]) * 180.0 / math.Pi
		if deg < 0 {
			deg += 360
		}
		fcAngles[i] = deg
	}

	temps, humid, speeds = res.temps[skip:], res.humid[skip:], res.speeds[skip:]
	precip := precipResult{amounts: res.precip.amounts[skip:], probabilities: res.precip.probabilities[skip:]}

	// Clamp forecasts to physical limits.
	clampSlice(temps, -50, 60)
	clampSlice(humid, 0, 100)
	clampSlice(speeds, 0, 300)

	model := "station-holtwinters (open-meteo not configured)"
	if omConfigured {
		model = "station-holtwinters (open-meteo unavailable)"
	}

	return &Forecast{
		GeneratedAt: e.generatedAt(),
		StationID:   e.cfg.StationID,
		Model:       model,
		// No weather-code, sunrise/sunset, cloud cover, or Kp source on this
		// path — buildDays degrades condition summaries to "Unknown" and
		// omits sunrise/sunset/aurora when these are nil.
		Days: buildDays(dayInput{
			now:    now,
			loc:    e.loc,
			temps:  temps,
			humid:  humid,
			speeds: speeds,
			angles: fcAngles,
			precip: precip,
		}),
	}
}

// Latest returns the most recently computed forecast, or nil, without
// checking its age or triggering a recompute.
func (e *Engine) Latest() *Forecast {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cached
}

// needsRefresh reports whether a cached forecast is missing or older than
// StaleAfter.
func (e *Engine) needsRefresh(f *Forecast) bool {
	return f == nil || e.now().Sub(f.GeneratedAt) > e.cfg.StaleAfter
}

// Fresh returns a forecast no older than StaleAfter, forcing a synchronous
// recompute first if the cache has gone stale (e.g. because the background
// scheduler missed ticks or the process was paused). Concurrent callers share
// a single recompute.
//
// The returned forecast's Stale field is separate from the forecast's own
// age, which the recompute has just reset: it reports whether the station
// itself has gone quiet (nothing stored within StaleAfter), or there is no
// data to forecast from at all.
func (e *Engine) Fresh() *Forecast {
	cached := e.Latest()
	if e.needsRefresh(cached) {
		e.computeMu.Lock()
		// Another caller may have recomputed while we waited for the lock.
		if cached = e.Latest(); e.needsRefresh(cached) {
			cached = e.computeLocked()
		}
		e.computeMu.Unlock()
	}

	result := *cached
	result.Stale = len(result.Days) == 0 || e.now().Sub(e.ts.UpdatedAt()) > e.cfg.StaleAfter
	return &result
}

// dayInput is everything buildDays needs to assemble the response. All the
// per-hour slices are indexed from the first forecast hour (the next whole
// hour after now) and must hold at least forecastHours values.
type dayInput struct {
	now time.Time

	// loc is the station's local timezone; days are its calendar days. Nil
	// means UTC.
	loc *time.Location

	temps, humid, speeds, angles []float64
	precip                       precipResult

	// codes may be nil (the station-only path has no weather-code source), in
	// which case condition summaries degrade to "Unknown" rather than being
	// omitted.
	codes []int

	// sunrises/sunsets are the sun events surrounding the forecast window.
	// Both may be nil, in which case Sunrise/Sunset are omitted and no hour
	// counts as dark.
	sunrises, sunsets []time.Time

	// cloudCover and kp must both be non-nil for aurora data to be computed
	// at all (the station-only path has neither); geomagLat is only
	// meaningful then.
	cloudCover, kp []float64
	geomagLat      float64
}

// daySpan is a run of consecutive forecast hours that fall on one local
// calendar date. first and last are inclusive hour indexes.
type daySpan struct {
	date        string // YYYY-MM-DD in the station's timezone
	first, last int
}

// buildDays assembles the response from per-hour forecast arrays, one entry
// per local calendar day: starting with today (possibly only its remaining
// hours), for at most forecastDays days. Hours that would spill onto a
// further day are dropped.
func buildDays(in dayInput) []DayForecast {
	loc := in.loc
	if loc == nil {
		loc = time.UTC
	}
	start := firstForecastHour(in.now)

	var spans []daySpan
	for idx := 0; idx < forecastHours; idx++ {
		date := start.Add(time.Duration(idx) * time.Hour).In(loc).Format("2006-01-02")
		if n := len(spans); n > 0 && spans[n-1].date == date {
			spans[n-1].last = idx
			continue
		}
		if len(spans) == forecastDays {
			break
		}
		spans = append(spans, daySpan{date: date, first: idx, last: idx})
	}

	b := dayBuilder{
		in:            in,
		loc:           loc,
		start:         start,
		sunriseByDate: localDateIndex(in.sunrises, loc),
		sunsetByDate:  localDateIndex(in.sunsets, loc),
		auroraOK:      in.cloudCover != nil && in.kp != nil,
	}
	if b.auroraOK {
		b.reqKp = requiredKp(in.geomagLat)
	}

	days := make([]DayForecast, 0, len(spans))
	for _, sp := range spans {
		days = append(days, b.build(sp))
	}
	return days
}

type dayBuilder struct {
	in                          dayInput
	loc                         *time.Location
	start                       time.Time
	sunriseByDate, sunsetByDate map[string]time.Time
	auroraOK                    bool
	reqKp                       float64
}

func (b *dayBuilder) codeAt(idx int) int {
	if idx < len(b.in.codes) {
		return b.in.codes[idx]
	}
	return -1 // no code available -> "Unknown"
}

func (b *dayBuilder) build(sp daySpan) DayForecast {
	in := b.in

	var sunrise, sunset *time.Time
	if sr, ok := b.sunriseByDate[sp.date]; ok {
		sunrise = &sr
	}
	if ss, ok := b.sunsetByDate[sp.date]; ok {
		sunset = &ss
	}

	var tH, hH, aH []HourlyPoint
	var wH []WindHourly
	var pH []PrecipHourly
	var cH []ConditionHourly
	var auH []AuroraHourly

	var tSum, hSum, sSum, sMax, aSum, auroraMax float64
	var dirAngles []float64

	// The day's representative condition is the one nearest local midday.
	// Partial days (today, once past noon) simply use their first hour.
	middayCode, middayDist := -1, math.MaxInt

	for idx := sp.first; idx <= sp.last; idx++ {
		t := b.start.Add(time.Duration(idx) * time.Hour)

		tH = append(tH, HourlyPoint{Time: t, Value: round2(in.temps[idx])})
		hH = append(hH, HourlyPoint{Time: t, Value: round2(in.humid[idx])})

		apparent := ApparentTemperatureC(in.temps[idx], in.humid[idx], in.speeds[idx])
		aH = append(aH, HourlyPoint{Time: t, Value: round2(apparent)})
		aSum += apparent

		code := b.codeAt(idx)
		cH = append(cH, ConditionHourly{Time: t, Code: code, Summary: conditionSummary(code)})
		if d := absInt(t.In(b.loc).Hour() - 12); d < middayDist {
			middayCode, middayDist = code, d
		}

		if b.auroraOK {
			isDark := isDarkAt(t, in.sunrises, in.sunsets)
			prob := auroraProbabilityPct(in.kp[idx], b.reqKp, in.cloudCover[idx], isDark)
			auH = append(auH, AuroraHourly{
				Time:           t,
				Kp:             round2(in.kp[idx]),
				RequiredKp:     round2(b.reqKp),
				CloudCoverPct:  round2(in.cloudCover[idx]),
				IsDark:         isDark,
				ProbabilityPct: round2(prob),
			})
			if prob > auroraMax {
				auroraMax = prob
			}
		}

		spd := round2(in.speeds[idx])
		dir := round2(in.angles[idx])
		wH = append(wH, WindHourly{
			Time:         t,
			SpeedKmh:     spd,
			DirectionDeg: dir,
			Cardinal:     degreeToCardinal(dir),
		})
		pH = append(pH, PrecipHourly{
			Time:        t,
			AmountMM:    round2(in.precip.amounts[idx]),
			Probability: round2(in.precip.probabilities[idx]),
		})

		tSum += in.temps[idx]
		hSum += in.humid[idx]
		sSum += in.speeds[idx]
		if in.speeds[idx] > sMax {
			sMax = in.speeds[idx]
		}
		dirAngles = append(dirAngles, in.angles[idx])
	}

	cnt := float64(len(tH))
	avgDir := circularMean(dirAngles)
	daySlice := func(s []float64) []float64 { return s[sp.first : sp.last+1] }

	var aurora *AuroraDay
	if b.auroraOK {
		aurora = &AuroraDay{MaxProbabilityPct: round2(auroraMax), Hourly: auH}
	}

	return DayForecast{
		Date:      sp.date,
		DayOfWeek: b.start.Add(time.Duration(sp.first) * time.Hour).In(b.loc).Format("Monday"),
		Sunrise:   sunrise,
		Sunset:    sunset,
		Aurora:    aurora,
		Condition: ConditionDay{
			Summary: conditionSummary(middayCode),
			Hourly:  cH,
		},
		Temperature: TemperatureDay{
			MinC:           round2(sliceMin(daySlice(in.temps))),
			MaxC:           round2(sliceMax(daySlice(in.temps))),
			AvgC:           round2(tSum / cnt),
			Hourly:         tH,
			ApparentAvgC:   round2(aSum / cnt),
			ApparentHourly: aH,
		},
		Humidity: HumidityDay{
			MinPct: round2(sliceMin(daySlice(in.humid))),
			MaxPct: round2(sliceMax(daySlice(in.humid))),
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
			TotalMM:     round2(sliceSum(daySlice(in.precip.amounts))),
			Probability: round2(sliceMax(daySlice(in.precip.probabilities))),
			Hourly:      pH,
		},
	}
}

// localDateIndex maps each event to the local calendar date it falls on.
func localDateIndex(events []time.Time, loc *time.Location) map[string]time.Time {
	m := make(map[string]time.Time, len(events))
	for _, ev := range events {
		m[ev.In(loc).Format("2006-01-02")] = ev
	}
	return m
}

// isDarkAt reports whether the sun is below the horizon at t. It follows the
// most recent sun event at or before t (a sunset means dark, a sunrise means
// light), so it never depends on which date an event is filed under. Before
// the first known event it assumes the opposite of that event. With no sun
// data at all it reports false.
func isDarkAt(t time.Time, sunrises, sunsets []time.Time) bool {
	var lastAt, firstAt time.Time
	var lastIsRise, firstIsRise, haveLast, haveFirst bool

	consider := func(at time.Time, isRise bool) {
		if !haveFirst || at.Before(firstAt) {
			firstAt, firstIsRise, haveFirst = at, isRise, true
		}
		if !at.After(t) && (!haveLast || at.After(lastAt)) {
			lastAt, lastIsRise, haveLast = at, isRise, true
		}
	}
	for _, at := range sunrises {
		consider(at, true)
	}
	for _, at := range sunsets {
		consider(at, false)
	}

	switch {
	case haveLast:
		return !lastIsRise
	case haveFirst:
		return firstIsRise
	default:
		return false
	}
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func hasNonFinite(s []float64) bool {
	for _, v := range s {
		if !isFinite(v) {
			return true
		}
	}
	return false
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
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
