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
