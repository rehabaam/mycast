package forecast

import (
	"testing"
	"time"

	"github.com/rehabaam/mycast/store"
)

func TestFreshReturnsCachedForecastWhenNotStale(t *testing.T) {
	ts := newTimeSeriesWithHistory(t, 72, 13.0, 80, 5.0)
	engine := NewEngine(ts, "test-station", nil, nil, 0, 0, time.Hour)

	first := engine.Compute()
	fresh := engine.Fresh()

	if fresh.Stale {
		t.Error("Stale = true, want false for a just-computed forecast")
	}
	if !fresh.GeneratedAt.Equal(first.GeneratedAt) {
		t.Errorf("Fresh() recomputed even though cache wasn't stale: GeneratedAt changed from %v to %v", first.GeneratedAt, fresh.GeneratedAt)
	}
}

func TestFreshRecomputesWhenCacheIsStale(t *testing.T) {
	ts := newTimeSeriesWithHistory(t, 72, 13.0, 80, 5.0)
	engine := NewEngine(ts, "test-station", nil, nil, 0, 0, time.Minute)

	first := engine.Compute()
	// Simulate a scheduler that stalled long ago by rewinding the cache's
	// GeneratedAt well past staleAfter.
	engine.mu.Lock()
	engine.cached.GeneratedAt = time.Now().Add(-time.Hour)
	engine.mu.Unlock()

	fresh := engine.Fresh()

	if fresh.Stale {
		t.Error("Stale = true, want false — Fresh() should have recomputed and produced a current forecast")
	}
	if !fresh.GeneratedAt.After(first.GeneratedAt) {
		t.Errorf("Fresh() did not recompute: GeneratedAt %v is not after original %v", fresh.GeneratedAt, first.GeneratedAt)
	}
}

func TestFreshComputesOnFirstCallWithNoPriorCache(t *testing.T) {
	ts := newTimeSeriesWithHistory(t, 72, 13.0, 80, 5.0)
	engine := NewEngine(ts, "test-station", nil, nil, 0, 0, time.Hour)

	fresh := engine.Fresh()
	if fresh == nil {
		t.Fatal("Fresh() returned nil with no prior Compute() call")
	}
	if fresh.Stale {
		t.Error("Stale = true, want false for a freshly computed forecast")
	}
}

func newTimeSeriesWithHistory(t *testing.T, hours int, temp, humidity, windSpeed float64) *store.TimeSeries {
	t.Helper()
	ts := store.NewTimeSeries(hours + 10)
	stationHistory(ts, hours, temp, humidity, windSpeed)
	return ts
}
