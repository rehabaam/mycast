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
	mu       sync.RWMutex
	data     []netatmo.Observation
	capacity int

	// lastMeasured is the newest measurement time (Unix seconds, before
	// hour-flooring) of any live reading accepted by Append, and updatedAt is
	// the wall-clock moment it was first seen.
	lastMeasured int64
	updatedAt    time.Time
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

// merge folds the field groups o actually carries into existing, leaving the
// rest of existing alone. An observation with no wind (Has lacks FieldWind)
// therefore can't zero out a wind value already stored for the hour.
func merge(existing, o netatmo.Observation) netatmo.Observation {
	has := o.Provides()
	out := existing
	out.Timestamp = o.Timestamp
	if has&netatmo.FieldOutdoor != 0 {
		out.Temperature, out.Humidity = o.Temperature, o.Humidity
	}
	if has&netatmo.FieldWind != 0 {
		out.WindSpeed, out.WindAngle = o.WindSpeed, o.WindAngle
		out.GustSpeed, out.GustAngle = o.GustSpeed, o.GustAngle
	}
	if has&netatmo.FieldRain != 0 {
		out.Rain = o.Rain
	}
	out.Has = existing.Provides() | has
	return out
}

// Append records a live reading from the station. Its timestamp is floored to
// the hour, so the buffer holds one slot per clock hour and its capacity
// really does span maxHours of wall-clock time: a later reading in the same
// hour is merged into the slot, field group by field group (last write wins
// for the groups it carries). Readings older than the newest stored hour are
// ignored.
//
// Append is what UpdatedAt tracks, but only a *newer measurement* counts as
// the station having reported: a reading whose measurement time does not
// advance past the newest one seen (the same data re-delivered, or a module
// that has gone quiet while its base keeps ticking) is recorded but leaves
// UpdatedAt alone, so the station still goes stale.
func (ts *TimeSeries) Append(obs ...netatmo.Observation) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, o := range obs {
		measured := o.Timestamp
		o.Timestamp = HourStart(measured)

		n := len(ts.data)
		switch {
		case n > 0 && o.Timestamp < ts.data[n-1].Timestamp:
			continue
		case n > 0 && o.Timestamp == ts.data[n-1].Timestamp:
			if measured < ts.lastMeasured {
				continue // an older measurement of the same hour
			}
			ts.data[n-1] = merge(ts.data[n-1], o)
		default:
			ts.data = append(ts.data, o)
		}

		if measured > ts.lastMeasured {
			ts.lastMeasured = measured
			ts.updatedAt = time.Now()
		}
	}
	ts.trim()
}

// Backfill merges authoritative hourly history, such as Netatmo's own
// per-hour aggregates. Each observation is merged into the bucket for its
// hour (or inserted in order), field group by field group: it corrects what
// it carries and leaves the rest. History that has no wind for an hour keeps
// the wind already stored for it; it does not turn it into a calm.
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
		if i < len(ts.data) && ts.data[i].Timestamp == o.Timestamp {
			ts.data[i] = merge(ts.data[i], o)
			continue
		}
		ts.data = append(ts.data, netatmo.Observation{})
		copy(ts.data[i+1:], ts.data[i:])
		ts.data[i] = o
	}
	ts.trim()
}

// Restore replaces the buffer's contents with state loaded from persistence:
// the stored observations, and the staleness bookkeeping (see UpdatedAt) that
// goes with them. It is how a short-lived process, such as a serverless
// invocation, resumes where the previous one left off. Observations are taken
// as stored: they are floored to the hour and put in order like any others.
func (ts *TimeSeries) Restore(obs []netatmo.Observation, lastMeasured int64, updatedAt time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	ts.data = ts.data[:0]
	ts.lastMeasured = lastMeasured
	ts.updatedAt = updatedAt
	for _, o := range obs {
		o.Timestamp = HourStart(o.Timestamp)
		ts.data = append(ts.data, o)
	}
	sort.SliceStable(ts.data, func(i, j int) bool { return ts.data[i].Timestamp < ts.data[j].Timestamp })

	// One slot per hour: if the input repeated an hour, the later entry wins.
	out := ts.data[:0]
	for _, o := range ts.data {
		if n := len(out); n > 0 && out[n-1].Timestamp == o.Timestamp {
			out[n-1] = o
			continue
		}
		out = append(out, o)
	}
	ts.data = out
	ts.trim()
}

// LastMeasured returns the newest live measurement time (Unix seconds, before
// hour-flooring) accepted by Append, or 0 if there has been none. Together
// with UpdatedAt it is the state Restore needs to carry across processes.
func (ts *TimeSeries) LastMeasured() int64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.lastMeasured
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

// UpdatedAt reports the wall-clock time at which the newest live measurement
// was first accepted (Append), or the zero time if the station has never
// reported. It tells a consumer how long it has been since the station last
// delivered anything new; history added with Backfill does not count, and
// neither does a reading that merely repeats an older measurement.
func (ts *TimeSeries) UpdatedAt() time.Time {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.updatedAt
}
