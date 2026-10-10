package main

import (
	"errors"
	"testing"
	"time"

	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/store"
)

type fakeSource struct {
	cur     *netatmo.Current
	curErr  error
	hist    []netatmo.Observation
	histErr error

	historyCalls int
	histStation  netatmo.Station
	histFrom     time.Time
	histTo       time.Time
}

func (f *fakeSource) GetCurrent() (*netatmo.Current, error) { return f.cur, f.curErr }

func (f *fakeSource) GetHistory(st netatmo.Station, from, to time.Time) ([]netatmo.Observation, error) {
	f.historyCalls++
	f.histStation = st
	f.histFrom, f.histTo = from, to
	return f.hist, f.histErr
}

// tickNow is 10:05 UTC: the 10:00 hour is in progress, 09:00 is complete.
var tickNow = time.Date(2026, time.June, 15, 10, 5, 0, 0, time.UTC)

func hourAt(h int) time.Time {
	return time.Date(2026, time.June, 15, h, 0, 0, 0, time.UTC)
}

// liveReading is an outdoor reading measured at 10:03, with the rolling
// last-hour rain sum Netatmo reports alongside it.
func liveReading(sumRain1h float64) *netatmo.Current {
	measured := tickNow.Add(-2 * time.Minute).Unix()
	return &netatmo.Current{
		Station:          netatmo.Station{ID: "70:ee:50:00:00:01", OutdoorModuleID: "02:00:00:00:00:01"},
		Timestamp:        measured,
		OutdoorTimestamp: measured,
		OutdoorAvailable: true,
		WindAvailable:    true,
		OutdoorTemp:      14,
		OutdoorHumidity:  70,
		WindSpeed:        6,
		SumRain1h:        sumRain1h,
	}
}

// historyPoint is a complete hourly aggregate, stamped mid-interval as
// Netatmo's are.
func historyPoint(hour int, temp, rain float64) netatmo.Observation {
	return netatmo.Observation{Timestamp: hourAt(hour).Add(30 * time.Minute).Unix(), Temperature: temp, Humidity: 60, Rain: rain}
}

// historyWithoutWind is an hourly aggregate for an hour in which the wind
// module reported nothing: its wind fields are zero, but not measured.
func historyWithoutWind(hour int, temp, rain float64) netatmo.Observation {
	o := historyPoint(hour, temp, rain)
	o.Has = netatmo.FieldOutdoor | netatmo.FieldRain
	return o
}

func newTickFixtures() (*store.CurrentCache, *store.TimeSeries, *forecast.Engine) {
	ts := store.NewTimeSeries(24 * 7)
	return &store.CurrentCache{}, ts, forecast.NewEngine(ts, forecast.Config{StationID: "st", StaleAfter: time.Hour})
}

func byHour(ts *store.TimeSeries) map[int64]netatmo.Observation {
	m := map[int64]netatmo.Observation{}
	for _, o := range ts.All() {
		m[o.Timestamp] = o
	}
	return m
}

func totalRain(ts *store.TimeSeries) (sum float64) {
	for _, o := range ts.All() {
		sum += o.Rain
	}
	return sum
}

func TestTickRecordsTheLiveReadingAndMarksTheStationAsReporting(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	src := &fakeSource{cur: liveReading(0)}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	got, ok := byHour(ts)[hourAt(10).Unix()]
	if !ok || got.Temperature != 14 || got.Humidity != 70 || got.WindSpeed != 6 {
		t.Errorf("10:00 bucket = %+v (present=%v), want the live reading", got, ok)
	}
	if ts.UpdatedAt().IsZero() {
		t.Error("UpdatedAt not set by a live reading")
	}
	if engine.Latest() == nil {
		t.Error("forecast was not recomputed")
	}
}

// The reported bug: the rolling hour sum is 09:05-10:05, which overlaps the
// completed 09:00 bucket. Storing it in the 10:00 bucket too counted that
// rain twice.
func TestTickDoesNotDoubleCountRain(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	// 0.5 mm fell at 09:30. Netatmo's completed 09:00 hour has it, and so
	// does the rolling sum measured at 10:03.
	src := &fakeSource{cur: liveReading(0.5), hist: []netatmo.Observation{historyPoint(9, 13, 0.5)}}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	if got := totalRain(ts); got != 0.5 {
		t.Errorf("total rain in the store = %v mm, want 0.5 (counted once)", got)
	}
	b := byHour(ts)
	if got := b[hourAt(9).Unix()].Rain; got != 0.5 {
		t.Errorf("09:00 rain = %v, want 0.5 from the hourly history", got)
	}
	if got := b[hourAt(10).Unix()].Rain; got != 0 {
		t.Errorf("10:00 (in progress) rain = %v, want 0 until the hour completes", got)
	}
}

func TestTickCorrectsCompletedHoursButNotTheHourInProgress(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	// The previous tick left a provisional 09:00 bucket with no rain.
	ts.Append(netatmo.Observation{Timestamp: hourAt(9).Add(40 * time.Minute).Unix(), Temperature: 12, Humidity: 65})

	src := &fakeSource{
		cur: liveReading(0),
		hist: []netatmo.Observation{
			historyPoint(8, 11, 0),
			historyPoint(9, 13, 0.6),  // authoritative: replaces the provisional bucket
			historyPoint(10, 99, 9.9), // the hour still in progress: must be ignored
		},
	}
	fetchAndUpdate(src, latest, ts, engine, tickNow)

	b := byHour(ts)
	if got := b[hourAt(9).Unix()]; got.Rain != 0.6 || got.Temperature != 13 {
		t.Errorf("09:00 = %+v, want the history's values (rain 0.6, temp 13)", got)
	}
	if got := b[hourAt(8).Unix()]; got.Temperature != 11 {
		t.Errorf("08:00 = %+v, want it filled in from history", got)
	}
	if got := b[hourAt(10).Unix()]; got.Temperature != 14 || got.Rain != 0 {
		t.Errorf("10:00 = %+v, want the live reading (14 °C, no rain), not history's partial hour", got)
	}

	if src.historyCalls != 1 {
		t.Fatalf("history fetched %d times, want once", src.historyCalls)
	}
	if want := tickNow.Add(-reconcileWindow); !src.histFrom.Equal(want) || !src.histTo.Equal(tickNow) {
		t.Errorf("history window = %v..%v, want %v..%v", src.histFrom, src.histTo, want, tickNow)
	}
}

func TestTickKeepsTheLiveReadingWhenHistoryFails(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	src := &fakeSource{cur: liveReading(0), histErr: errors.New("netatmo history down")}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	if _, ok := byHour(ts)[hourAt(10).Unix()]; !ok {
		t.Error("live reading was dropped because the history refresh failed")
	}
	if ts.UpdatedAt().IsZero() {
		t.Error("UpdatedAt not set")
	}
}

func TestTickSkipsAnUnavailableOutdoorModule(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	cur := liveReading(0)
	cur.OutdoorAvailable = false
	cur.OutdoorTemp, cur.OutdoorHumidity = 0, 0
	src := &fakeSource{cur: cur}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	if ts.Len() != 0 {
		t.Errorf("store has %d observations, want none (no fake 0 °C reading)", ts.Len())
	}
	if !ts.UpdatedAt().IsZero() {
		t.Error("an unavailable module counted as the station reporting")
	}
	if src.historyCalls != 0 {
		t.Errorf("history fetched %d times for a skipped tick", src.historyCalls)
	}
}

func TestTickLeavesTheStoreAloneWhenTheFetchFails(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	src := &fakeSource{curErr: errors.New("netatmo down")}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	if ts.Len() != 0 || !ts.UpdatedAt().IsZero() || src.historyCalls != 0 {
		t.Errorf("a failed fetch changed state: len=%d updated=%v historyCalls=%d", ts.Len(), ts.UpdatedAt(), src.historyCalls)
	}
}

// The reported bug: reconciling replaced the whole stored record, so an hour
// for which the wind module had no aggregate wiped the live wind to zero.
func TestTickKeepsLiveWindWhenHistoryHasNoWindForTheHour(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	// The previous tick stored a real 14 km/h wind in the 09:00 bucket.
	ts.Append(netatmo.Observation{
		Timestamp:   hourAt(9).Add(40 * time.Minute).Unix(),
		Temperature: 12, Humidity: 65, WindSpeed: 14, WindAngle: 220,
		Has: netatmo.FieldOutdoor | netatmo.FieldWind,
	})
	src := &fakeSource{cur: liveReading(0), hist: []netatmo.Observation{historyWithoutWind(9, 13, 0.6)}}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	got := byHour(ts)[hourAt(9).Unix()]
	if got.WindSpeed != 14 || got.WindAngle != 220 {
		t.Errorf("09:00 wind = %v km/h @ %v°, want the stored 14 @ 220 (history had none)", got.WindSpeed, got.WindAngle)
	}
	if got.Temperature != 13 || got.Rain != 0.6 {
		t.Errorf("09:00 temp/rain = %v/%v, want history's 13 / 0.6", got.Temperature, got.Rain)
	}
}

// The reported masking: the base station keeps ticking, but the outdoor
// module has gone quiet and re-delivers the same measurement. That must not
// make the station look like it is still reporting.
func TestTickDoesNotRefreshStalenessForAQuietOutdoorModule(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	quiet := liveReading(0)
	src := &fakeSource{cur: quiet}

	fetchAndUpdate(src, latest, ts, engine, tickNow)
	first := ts.UpdatedAt()
	if first.IsZero() {
		t.Fatal("first reading did not mark the station as reporting")
	}

	time.Sleep(15 * time.Millisecond)
	// Half an hour later the base has a new time, but the outdoor module's
	// measurement is exactly the one we already had.
	again := *quiet
	again.Timestamp += 30 * 60
	src.cur = &again
	fetchAndUpdate(src, latest, ts, engine, tickNow.Add(30*time.Minute))

	if got := ts.UpdatedAt(); !got.Equal(first) {
		t.Errorf("UpdatedAt moved from %v to %v although the outdoor module reported nothing new", first, got)
	}
}

// A module that is flagged reachable but last measured hours ago must have its
// values filed under the hour they belong to, not under the base's current one.
func TestTickFilesAStaleOutdoorReadingUnderItsOwnHour(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	cur := liveReading(0)
	cur.OutdoorTimestamp = tickNow.Add(-3 * time.Hour).Unix() // measured at 07:05
	src := &fakeSource{cur: cur}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	b := byHour(ts)
	if _, ok := b[hourAt(10).Unix()]; ok {
		t.Error("an outdoor value from 07:05 was filed under the base's 10:00 hour")
	}
	if got, ok := b[hourAt(7).Unix()]; !ok || got.Temperature != 14 {
		t.Errorf("07:00 bucket = %+v (present=%v), want the old reading under its own hour", got, ok)
	}
}

func TestTickCachesTheReadingEvenWhenTheOutdoorModuleIsUnavailable(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	cur := liveReading(0)
	cur.OutdoorAvailable = false
	src := &fakeSource{cur: cur}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	got, ok := latest.Latest()
	if !ok {
		t.Fatal("the reading was not cached: /current would have nothing to serve")
	}
	if got.OutdoorAvailable {
		t.Error("cached reading claims the outdoor module is available")
	}
	if ts.Len() != 0 {
		t.Errorf("store has %d observations, want none", ts.Len())
	}
}

func TestTickAsksForHistoryOfTheStationItJustDescribed(t *testing.T) {
	latest, ts, engine := newTickFixtures()
	src := &fakeSource{cur: liveReading(0)}

	fetchAndUpdate(src, latest, ts, engine, tickNow)

	if src.histStation.ID != "70:ee:50:00:00:01" || src.histStation.OutdoorModuleID != "02:00:00:00:00:01" {
		t.Errorf("history requested for %+v, want the station from this tick's reading", src.histStation)
	}
}
