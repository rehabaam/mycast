package store

import (
	"testing"
	"time"

	"github.com/rehabaam/mycast/netatmo"
)

// hourTS returns the Unix timestamp of `hour` hours after an arbitrary
// hour-aligned origin, plus `sec` seconds.
func hourTS(hour int, sec int64) int64 {
	const origin = 1_780_000_000 - 1_780_000_000%3600
	return origin + int64(hour)*3600 + sec
}

func obsTemp(ts int64, temp float64) netatmo.Observation {
	return netatmo.Observation{Timestamp: ts, Temperature: temp}
}

func temps(ts *TimeSeries) []float64 {
	var out []float64
	for _, o := range ts.All() {
		out = append(out, o.Temperature)
	}
	return out
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAppendFloorsTimestampsToTheHour(t *testing.T) {
	ts := NewTimeSeries(10)
	// Netatmo's hourly history is stamped mid-interval (e.g. 14:30).
	ts.Append(obsTemp(hourTS(0, 1800), 10), obsTemp(hourTS(1, 1800), 11))

	all := ts.All()
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}
	for i, o := range all {
		if want := hourTS(i, 0); o.Timestamp != want {
			t.Errorf("all[%d].Timestamp = %d, want %d (hour-aligned)", i, o.Timestamp, want)
		}
	}
}

func TestAppendKeepsOneSlotPerHourLastWriteWins(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsTemp(hourTS(0, 0), 10))
	// A live reading in the same hour as the latest history point replaces
	// it instead of adding a second slot.
	ts.Append(obsTemp(hourTS(0, 1500), 12))
	ts.Append(obsTemp(hourTS(0, 3000), 13))

	if got := temps(ts); !equalFloats(got, []float64{13}) {
		t.Errorf("temps = %v, want [13]", got)
	}
}

func TestAppendIgnoresObservationsOlderThanTheNewest(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsTemp(hourTS(5, 0), 5))
	ts.Append(obsTemp(hourTS(3, 0), 3), obsTemp(hourTS(4, 3599), 4))

	if got := temps(ts); !equalFloats(got, []float64{5}) {
		t.Errorf("temps = %v, want [5]", got)
	}
}

func TestAppendKeepsAscendingOrder(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsTemp(hourTS(0, 0), 0), obsTemp(hourTS(1, 0), 1), obsTemp(hourTS(2, 0), 2))
	ts.Append(obsTemp(hourTS(3, 0), 3))

	all := ts.All()
	for i := 1; i < len(all); i++ {
		if all[i].Timestamp <= all[i-1].Timestamp {
			t.Fatalf("timestamps not strictly ascending at %d: %v", i, all)
		}
	}
}

func TestCapacityCoversWallClockHours(t *testing.T) {
	// A week's capacity must really hold a week of hours even when a live
	// reading is appended several times an hour.
	const cap = 48
	ts := NewTimeSeries(cap)
	for h := 0; h < 100; h++ {
		for _, sec := range []int64{0, 900, 1800, 2700} {
			ts.Append(obsTemp(hourTS(h, sec), float64(h)))
		}
	}

	all := ts.All()
	if len(all) != cap {
		t.Fatalf("len = %d, want %d", len(all), cap)
	}
	if first, last := all[0].Temperature, all[len(all)-1].Temperature; first != 52 || last != 99 {
		t.Errorf("window = hours %v..%v, want 52..99 (the newest 48 hours)", first, last)
	}
}

func TestAllReturnsACopy(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsTemp(hourTS(0, 0), 1))
	all := ts.All()
	all[0].Temperature = 99
	if got := ts.All()[0].Temperature; got != 1 {
		t.Errorf("mutating the returned slice changed the store: %v", got)
	}
}

func TestUpdatedAt(t *testing.T) {
	ts := NewTimeSeries(10)
	if !ts.UpdatedAt().IsZero() {
		t.Fatalf("UpdatedAt = %v, want zero before any data", ts.UpdatedAt())
	}

	before := time.Now()
	ts.Append(obsTemp(hourTS(1, 0), 1))
	after := time.Now()
	got := ts.UpdatedAt()
	if got.Before(before) || got.After(after) {
		t.Errorf("UpdatedAt = %v, want within [%v, %v]", got, before, after)
	}

	// Ignored observations don't count as the station having reported.
	ts.Append(obsTemp(hourTS(0, 0), 0))
	if !ts.UpdatedAt().Equal(got) {
		t.Errorf("UpdatedAt moved after an ignored append: %v -> %v", got, ts.UpdatedAt())
	}
}

func TestConcurrentAppendAndRead(t *testing.T) {
	ts := NewTimeSeries(24)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for h := 0; h < 500; h++ {
			ts.Append(obsTemp(hourTS(h, 0), float64(h)))
		}
	}()
	for i := 0; i < 500; i++ {
		_ = ts.All()
		_ = ts.Len()
		_ = ts.UpdatedAt()
	}
	<-done
}

func TestBackfillInsertsInOrderReplacesAndExtendsIntoThePast(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Backfill(obsTemp(hourTS(1, 0), 1), obsTemp(hourTS(3, 0), 3))

	ts.Backfill(obsTemp(hourTS(2, 1800), 2)) // fills the gap, in order
	ts.Backfill(obsTemp(hourTS(3, 600), 33)) // replaces an existing hour
	ts.Backfill(obsTemp(hourTS(0, 0), 0))    // older than everything stored
	ts.Backfill(obsTemp(hourTS(5, 0), 5))    // newer than everything stored

	if got := temps(ts); !equalFloats(got, []float64{0, 1, 2, 33, 5}) {
		t.Errorf("temps = %v, want [0 1 2 33 5]", got)
	}
	all := ts.All()
	for i := 1; i < len(all); i++ {
		if all[i].Timestamp <= all[i-1].Timestamp {
			t.Fatalf("timestamps not strictly ascending: %v", all)
		}
	}
}

func TestBackfillKeepsTheNewestWhenOverCapacity(t *testing.T) {
	ts := NewTimeSeries(3)
	for h := 0; h < 6; h++ {
		ts.Backfill(obsTemp(hourTS(h, 0), float64(h)))
	}
	if got := temps(ts); !equalFloats(got, []float64{3, 4, 5}) {
		t.Errorf("temps = %v, want the newest three [3 4 5]", got)
	}
}

// Replaying history, at startup or on a refresh, says nothing about whether
// the station is reporting now. Only live readings may clear "stale".
func TestBackfillDoesNotCountAsTheStationReporting(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Backfill(obsTemp(hourTS(0, 0), 0), obsTemp(hourTS(1, 0), 1))

	if !ts.UpdatedAt().IsZero() {
		t.Fatalf("UpdatedAt = %v after Backfill only, want zero", ts.UpdatedAt())
	}
	if ts.Len() != 2 {
		t.Errorf("Len = %d, want 2", ts.Len())
	}

	ts.Append(obsTemp(hourTS(2, 0), 2))
	if ts.UpdatedAt().IsZero() {
		t.Error("UpdatedAt still zero after a live Append")
	}
}

func TestAppendReplacesTheNewestBackfilledHour(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Backfill(obsTemp(hourTS(0, 0), 10), obsTemp(hourTS(1, 0), 11))
	ts.Append(obsTemp(hourTS(1, 1200), 15)) // the live reading in the same hour wins

	if got := temps(ts); !equalFloats(got, []float64{10, 15}) {
		t.Errorf("temps = %v, want [10 15]", got)
	}
}

func TestHourStart(t *testing.T) {
	for in, want := range map[int64]int64{0: 0, 3599: 0, 3600: 3600, 7199: 3600, 1781003599: 1781002800} {
		if got := HourStart(in); got != want {
			t.Errorf("HourStart(%d) = %d, want %d", in, got, want)
		}
	}
}

// obsFields builds a partial observation: only the named groups are real.
func obsFields(ts int64, has netatmo.Fields, temp, wind, rain float64) netatmo.Observation {
	return netatmo.Observation{Timestamp: ts, Temperature: temp, WindSpeed: wind, Rain: rain, Has: has}
}

func onlyHour(t *testing.T, ts *TimeSeries) netatmo.Observation {
	t.Helper()
	all := ts.All()
	if len(all) != 1 {
		t.Fatalf("store has %d observations, want 1", len(all))
	}
	return all[0]
}

// The reported bug: reconciling history replaced the whole stored record, so
// an hour with no wind aggregate wiped a good live wind value to zero.
func TestBackfillWithoutWindKeepsTheStoredWind(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsFields(hourTS(0, 600), netatmo.FieldOutdoor|netatmo.FieldWind, 12, 14, 0))

	// History for the same hour has temperature and rain but no wind.
	ts.Backfill(obsFields(hourTS(0, 1800), netatmo.FieldOutdoor|netatmo.FieldRain, 13, 0, 0.6))

	got := onlyHour(t, ts)
	if got.WindSpeed != 14 {
		t.Errorf("WindSpeed = %v, want the stored 14 (history had no wind)", got.WindSpeed)
	}
	if got.Temperature != 13 || got.Rain != 0.6 {
		t.Errorf("Temperature=%v Rain=%v, want 13 / 0.6 from history", got.Temperature, got.Rain)
	}
}

func TestBackfillWithWindOverwritesTheStoredWind(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsFields(hourTS(0, 600), netatmo.FieldOutdoor|netatmo.FieldWind, 12, 14, 0))
	ts.Backfill(obsFields(hourTS(0, 1800), netatmo.FieldWind, 0, 9, 0))

	got := onlyHour(t, ts)
	if got.WindSpeed != 9 {
		t.Errorf("WindSpeed = %v, want history's 9", got.WindSpeed)
	}
	if got.Temperature != 12 {
		t.Errorf("Temperature = %v, want the stored 12 untouched", got.Temperature)
	}
}

// A measured calm is data, unlike a missing value.
func TestAMeasuredCalmOverwritesWind(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsFields(hourTS(0, 600), netatmo.FieldOutdoor|netatmo.FieldWind, 12, 14, 0))
	ts.Backfill(obsFields(hourTS(0, 1800), netatmo.FieldWind, 0, 0, 0))

	if got := onlyHour(t, ts).WindSpeed; got != 0 {
		t.Errorf("WindSpeed = %v, want 0: a measured calm replaces the stored value", got)
	}
}

// A live reading in the hour that history already reconciled must not wipe
// the reconciled rain, since live readings carry no rain.
func TestLiveReadingDoesNotWipeReconciledRain(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Backfill(obsFields(hourTS(0, 1800), netatmo.FieldOutdoor|netatmo.FieldWind|netatmo.FieldRain, 11, 5, 0.8))
	ts.Append(obsFields(hourTS(0, 3000), netatmo.FieldOutdoor|netatmo.FieldWind, 12, 7, 0))

	got := onlyHour(t, ts)
	if got.Rain != 0.8 {
		t.Errorf("Rain = %v, want the reconciled 0.8", got.Rain)
	}
	if got.Temperature != 12 || got.WindSpeed != 7 {
		t.Errorf("Temperature=%v WindSpeed=%v, want the live 12 / 7", got.Temperature, got.WindSpeed)
	}
}

func TestInsertedPartialObservationRecordsWhatItHas(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Backfill(obsFields(hourTS(0, 0), netatmo.FieldOutdoor, 12, 0, 0))

	got := onlyHour(t, ts)
	if got.Has != netatmo.FieldOutdoor {
		t.Errorf("Has = %b, want outdoor only", got.Has)
	}
	// A later wind reading fills it in.
	ts.Backfill(obsFields(hourTS(0, 0), netatmo.FieldWind, 0, 8, 0))
	if got = onlyHour(t, ts); got.Has != netatmo.FieldOutdoor|netatmo.FieldWind || got.WindSpeed != 8 || got.Temperature != 12 {
		t.Errorf("after wind arrives: %+v", got)
	}
}

// The reported masking: a module that has gone quiet is re-delivered with the
// same measurement time while the base keeps the service ticking. That is not
// the station reporting.
func TestARepeatedMeasurementDoesNotRefreshUpdatedAt(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsTemp(hourTS(1, 600), 12))
	first := ts.UpdatedAt()
	if first.IsZero() {
		t.Fatal("UpdatedAt not set by the first reading")
	}

	time.Sleep(15 * time.Millisecond)
	ts.Append(obsTemp(hourTS(1, 600), 12)) // the very same measurement again
	ts.Append(obsTemp(hourTS(1, 100), 12)) // an older measurement of the hour

	if got := ts.UpdatedAt(); !got.Equal(first) {
		t.Errorf("UpdatedAt moved from %v to %v with no newer measurement", first, got)
	}
}

func TestANewerMeasurementAdvancesUpdatedAt(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsTemp(hourTS(1, 600), 12))
	first := ts.UpdatedAt()

	time.Sleep(15 * time.Millisecond)
	ts.Append(obsTemp(hourTS(1, 1200), 13)) // same hour, ten minutes later

	if got := ts.UpdatedAt(); !got.After(first) {
		t.Errorf("UpdatedAt = %v, want after %v for a newer measurement", got, first)
	}
	if got := temps(ts); !equalFloats(got, []float64{13}) {
		t.Errorf("temps = %v, want the newer reading [13]", got)
	}
}

func TestAnOlderMeasurementOfTheSameHourDoesNotReplaceANewerOne(t *testing.T) {
	ts := NewTimeSeries(10)
	ts.Append(obsTemp(hourTS(1, 1200), 13))
	ts.Append(obsTemp(hourTS(1, 100), 99))

	if got := temps(ts); !equalFloats(got, []float64{13}) {
		t.Errorf("temps = %v, want [13]: an older measurement must not win", got)
	}
}

func TestCurrentCache(t *testing.T) {
	var c CurrentCache
	if cur, ok := c.Latest(); ok || cur != nil {
		t.Fatalf("Latest on an empty cache = (%v, %v), want (nil, false)", cur, ok)
	}

	in := &netatmo.Current{OutdoorTemp: 16.6, Modules: []netatmo.ModuleStatus{{Name: "Outdoor"}}}
	c.Put(in)

	// The cache keeps its own copy, in both directions.
	in.OutdoorTemp = 99
	in.Modules[0].Name = "mutated by the writer"
	got, ok := c.Latest()
	if !ok || got.OutdoorTemp != 16.6 || got.Modules[0].Name != "Outdoor" {
		t.Fatalf("Latest = (%+v, %v), want the value as it was Put", got, ok)
	}
	got.OutdoorTemp = 77
	got.Modules[0].Name = "mutated by a reader"
	if again, _ := c.Latest(); again.OutdoorTemp != 16.6 || again.Modules[0].Name != "Outdoor" {
		t.Errorf("a reader's mutation leaked into the cache: %+v", again)
	}
}

func TestCurrentCacheIsSafeForConcurrentUse(t *testing.T) {
	var c CurrentCache
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			c.Put(&netatmo.Current{OutdoorTemp: float64(i)})
		}
	}()
	for i := 0; i < 500; i++ {
		c.Latest()
	}
	<-done
}
