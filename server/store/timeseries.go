package store

import (
	"sort"
	"sync"
	"time"

	"github.com/rehabaam/mycast/netatmo"
)

// TimeSeries is a thread-safe buffer of hourly Observations: at most one per
// clock hour, in ascending time order, with the oldest dropped at capacity.
type TimeSeries struct {
	mu        sync.RWMutex
	data      []netatmo.Observation
	capacity  int
	updatedAt time.Time
}

// NewTimeSeries creates a buffer that holds maxHours of observations.
func NewTimeSeries(maxHours int) *TimeSeries {
	return &TimeSeries{
		data:     make([]netatmo.Observation, 0, maxHours),
		capacity: maxHours,
	}
}

// HourStart truncates a Unix timestamp to the start of its clock hour. It is
// the bucketing rule the store applies to every observation.
func HourStart(ts int64) int64 {
	return ts - ts%3600
}

// Append records a live reading from the station. Its timestamp is floored to
// the hour, so the buffer holds one slot per clock hour and its capacity
// really does span maxHours of wall-clock time: a later reading in the same
// hour replaces the earlier one (last write wins). Readings older than the
// newest stored hour are ignored.
//
// Only Append counts as the station having reported: it is what UpdatedAt
// tracks.
func (ts *TimeSeries) Append(obs ...netatmo.Observation) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, o := range obs {
		o.Timestamp = HourStart(o.Timestamp)

		n := len(ts.data)
		switch {
		case n > 0 && o.Timestamp < ts.data[n-1].Timestamp:
			continue
		case n > 0 && o.Timestamp == ts.data[n-1].Timestamp:
			ts.data[n-1] = o
		default:
			ts.data = append(ts.data, o)
		}
		ts.updatedAt = time.Now()
	}
	ts.trim()
}

// Backfill merges authoritative hourly history, such as Netatmo's own
// per-hour aggregates. Each observation replaces the bucket for its hour or
// is inserted in order, so it can correct hours already stored (for example
// the provisional values of a live reading) as well as extend the past.
//
// Backfill deliberately does not touch UpdatedAt: replaying old data at
// startup, or refreshing recent hours, says nothing about whether the station
// is currently reporting.
func (ts *TimeSeries) Backfill(obs ...netatmo.Observation) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, o := range obs {
		o.Timestamp = HourStart(o.Timestamp)

		i := sort.Search(len(ts.data), func(i int) bool { return ts.data[i].Timestamp >= o.Timestamp })
		switch {
		case i < len(ts.data) && ts.data[i].Timestamp == o.Timestamp:
			ts.data[i] = o
		default:
			ts.data = append(ts.data, netatmo.Observation{})
			copy(ts.data[i+1:], ts.data[i:])
			ts.data[i] = o
		}
	}
	ts.trim()
}

// trim drops the oldest observations beyond capacity. The caller holds mu.
func (ts *TimeSeries) trim() {
	if len(ts.data) > ts.capacity {
		ts.data = ts.data[len(ts.data)-ts.capacity:]
	}
}

// All returns a copy of all stored observations in ascending time order, one
// per hour (hours with no data are simply absent).
func (ts *TimeSeries) All() []netatmo.Observation {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]netatmo.Observation, len(ts.data))
	copy(out, ts.data)
	return out
}

// Len returns the number of stored observations.
func (ts *TimeSeries) Len() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.data)
}

// UpdatedAt reports the wall-clock time of the last accepted live reading
// (Append), or the zero time if the station has never reported. It tells a
// consumer how long it has been since the station last delivered data;
// history added with Backfill does not count.
func (ts *TimeSeries) UpdatedAt() time.Time {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.updatedAt
}
