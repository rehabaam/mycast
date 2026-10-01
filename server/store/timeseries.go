package store

import (
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

// floorToHour truncates a Unix timestamp to the start of its hour.
func floorToHour(ts int64) int64 {
	return ts - ts%3600
}

// Append adds one or more observations. Timestamps are floored to the hour,
// so the buffer holds one slot per clock hour and its capacity really does
// span maxHours of wall-clock time: a later observation in the same hour
// replaces the earlier one (last write wins). Observations older than the
// newest stored hour are ignored.
func (ts *TimeSeries) Append(obs ...netatmo.Observation) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, o := range obs {
		o.Timestamp = floorToHour(o.Timestamp)

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

// UpdatedAt reports the wall-clock time of the last accepted Append, or the
// zero time if nothing has ever been stored. It tells a consumer how long it
// has been since the station last delivered data.
func (ts *TimeSeries) UpdatedAt() time.Time {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.updatedAt
}
