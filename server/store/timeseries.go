package store

import (
	"sync"
	"time"

	"github.com/rehabaam/mycast/netatmo"
)

// TimeSeries is a thread-safe circular buffer of hourly Observations.
type TimeSeries struct {
	mu       sync.RWMutex
	data     []netatmo.Observation
	capacity int
}

// NewTimeSeries creates a buffer that holds maxHours of observations.
func NewTimeSeries(maxHours int) *TimeSeries {
	return &TimeSeries{
		data:     make([]netatmo.Observation, 0, maxHours),
		capacity: maxHours,
	}
}

// Append adds one or more observations, keeping the buffer sorted by timestamp
// and within capacity (oldest entries are dropped).
func (ts *TimeSeries) Append(obs ...netatmo.Observation) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, o := range obs {
		// Skip if duplicate or older than the last stored entry.
		if len(ts.data) > 0 && o.Timestamp <= ts.data[len(ts.data)-1].Timestamp {
			// Update in-place if same timestamp (refresh current hour).
			if len(ts.data) > 0 && o.Timestamp == ts.data[len(ts.data)-1].Timestamp {
				ts.data[len(ts.data)-1] = o
			}
			continue
		}
		ts.data = append(ts.data, o)
	}

	// Trim to capacity.
	if len(ts.data) > ts.capacity {
		ts.data = ts.data[len(ts.data)-ts.capacity:]
	}
}

// All returns a copy of all stored observations in ascending time order.
func (ts *TimeSeries) All() []netatmo.Observation {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]netatmo.Observation, len(ts.data))
	copy(out, ts.data)
	return out
}

// Since returns observations with timestamps >= the given time.
func (ts *TimeSeries) Since(t time.Time) []netatmo.Observation {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	unix := t.Unix()
	for i, o := range ts.data {
		if o.Timestamp >= unix {
			out := make([]netatmo.Observation, len(ts.data)-i)
			copy(out, ts.data[i:])
			return out
		}
	}
	return nil
}

// Len returns the number of stored observations.
func (ts *TimeSeries) Len() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.data)
}

// Temperatures extracts the temperature time series as a plain float64 slice.
func (ts *TimeSeries) Temperatures() []float64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]float64, len(ts.data))
	for i, o := range ts.data {
		out[i] = o.Temperature
	}
	return out
}

// Humidities extracts the humidity time series.
func (ts *TimeSeries) Humidities() []float64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]float64, len(ts.data))
	for i, o := range ts.data {
		out[i] = o.Humidity
	}
	return out
}

// WindSpeeds extracts the wind speed time series.
func (ts *TimeSeries) WindSpeeds() []float64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]float64, len(ts.data))
	for i, o := range ts.data {
		out[i] = o.WindSpeed
	}
	return out
}

// WindAngles extracts the wind angle time series.
func (ts *TimeSeries) WindAngles() []float64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]float64, len(ts.data))
	for i, o := range ts.data {
		out[i] = o.WindAngle
	}
	return out
}

// Rains extracts the precipitation time series.
func (ts *TimeSeries) Rains() []float64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]float64, len(ts.data))
	for i, o := range ts.data {
		out[i] = o.Rain
	}
	return out
}
