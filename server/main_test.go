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
	histFrom     time.Time
	histTo       time.Time
}

func (f *fakeSource) GetCurrent() (*netatmo.Current, error) { return f.cur, f.curErr }

func (f *fakeSource) GetHistory(from, to time.Time) ([]netatmo.Observation, error) {
	f.historyCalls++
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
	return &netatmo.Current{
		Timestamp:        tickNow.Add(-2 * time.Minute).Unix(),
		OutdoorAvailable: true,
		OutdoorTemp:      14,
		OutdoorHumidity:  70,
		WindSpeed:        6,
		SumRain1h:        sumRain1h,
	}
}

// historyPoint is stamped mid-interval, as Netatmo's hourly aggregates are.
func historyPoint(hour int, temp, rain float64) netatmo.Observation {
	return netatmo.Observation{Timestamp: hourAt(hour).Add(30 * time.Minute).Unix(), Temperature: temp, Humidity: 60, Rain: rain}
}

func newTickFixtures() (*store.TimeSeries, *forecast.Engine) {
	ts := store.NewTimeSeries(24 * 7)
	return ts, forecast.NewEngine(ts, forecast.Config{StationID: "st", StaleAfter: time.Hour})
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
	ts, engine := newTickFixtures()
	src := &fakeSource{cur: liveReading(0)}

	fetchAndUpdate(src, ts, engine, tickNow)

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
	ts, engine := newTickFixtures()
	// 0.5 mm fell at 09:30. Netatmo's completed 09:00 hour has it, and so
	// does the rolling sum measured at 10:03.
	src := &fakeSource{cur: liveReading(0.5), hist: []netatmo.Observation{historyPoint(9, 13, 0.5)}}

	fetchAndUpdate(src, ts, engine, tickNow)

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
	ts, engine := newTickFixtures()
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
	fetchAndUpdate(src, ts, engine, tickNow)

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
	ts, engine := newTickFixtures()
	src := &fakeSource{cur: liveReading(0), histErr: errors.New("netatmo history down")}

	fetchAndUpdate(src, ts, engine, tickNow)

	if _, ok := byHour(ts)[hourAt(10).Unix()]; !ok {
		t.Error("live reading was dropped because the history refresh failed")
	}
	if ts.UpdatedAt().IsZero() {
		t.Error("UpdatedAt not set")
	}
}

func TestTickSkipsAnUnavailableOutdoorModule(t *testing.T) {
	ts, engine := newTickFixtures()
	cur := liveReading(0)
	cur.OutdoorAvailable = false
	cur.OutdoorTemp, cur.OutdoorHumidity = 0, 0
	src := &fakeSource{cur: cur}

	fetchAndUpdate(src, ts, engine, tickNow)

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
	ts, engine := newTickFixtures()
	src := &fakeSource{curErr: errors.New("netatmo down")}

	fetchAndUpdate(src, ts, engine, tickNow)

	if ts.Len() != 0 || !ts.UpdatedAt().IsZero() || src.historyCalls != 0 {
		t.Errorf("a failed fetch changed state: len=%d updated=%v historyCalls=%d", ts.Len(), ts.UpdatedAt(), src.historyCalls)
	}
}
