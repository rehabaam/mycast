package forecast

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rehabaam/mycast/openmeteo"
	"github.com/rehabaam/mycast/store"
)

func TestFreshReturnsCachedForecastWhenNotStale(t *testing.T) {
	ts := newTimeSeriesWithHistory(t, 72, 13.0, 80, 5.0)
	engine := NewEngine(ts, Config{StationID: "test-station", StaleAfter: time.Hour})

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
	engine := NewEngine(ts, Config{StationID: "test-station", StaleAfter: time.Minute})

	engine.Compute()
	// Simulate a scheduler that stalled long ago by rewinding the cache's
	// GeneratedAt well past StaleAfter.
	rewound := time.Now().Add(-time.Hour)
	engine.mu.Lock()
	engine.cached.GeneratedAt = rewound
	engine.mu.Unlock()

	fresh := engine.Fresh()

	if fresh.Stale {
		t.Error("Stale = true, want false — the station reported just now, so a recompute gives a current forecast")
	}
	if !fresh.GeneratedAt.After(rewound) {
		t.Errorf("Fresh() did not recompute: GeneratedAt %v is not after the rewound %v", fresh.GeneratedAt, rewound)
	}
}

func TestFreshComputesOnFirstCallWithNoPriorCache(t *testing.T) {
	ts := newTimeSeriesWithHistory(t, 72, 13.0, 80, 5.0)
	engine := NewEngine(ts, Config{StationID: "test-station", StaleAfter: time.Hour})

	fresh := engine.Fresh()
	if fresh == nil {
		t.Fatal("Fresh() returned nil with no prior Compute() call")
	}
	if fresh.Stale {
		t.Error("Stale = true, want false for a freshly computed forecast")
	}
}

func TestFreshFlagsASilentStationAsStale(t *testing.T) {
	ts := newTimeSeriesWithHistory(t, 72, 13.0, 80, 5.0)
	engine := NewEngine(ts, Config{StationID: "test-station", StaleAfter: time.Minute})
	// The station last delivered data "now"; the engine's clock says an hour
	// has passed since.
	engine.now = func() time.Time { return time.Now().Add(time.Hour) }

	fresh := engine.Fresh()

	if !fresh.Stale {
		t.Error("Stale = false, want true — nothing has been stored for longer than StaleAfter")
	}
	if len(fresh.Days) == 0 {
		t.Error("Days is empty, want the (stale) forecast still served")
	}
	if !fresh.GeneratedAt.After(time.Now().Add(30 * time.Minute)) {
		t.Errorf("GeneratedAt = %v: the recompute should still have happened", fresh.GeneratedAt)
	}
}

// The reported scenario: the process starts while the outdoor module is
// offline, so only the history backfill happens. That old data must not make
// the station look like it is reporting.
func TestFreshTreatsBackfilledHistoryAloneAsStale(t *testing.T) {
	ts := store.NewTimeSeries(24 * 10)
	now := time.Now()
	for h := 72; h >= 1; h-- {
		ts.Backfill(obsAtTime(now.Add(-time.Duration(h)*time.Hour), 13, 80, 5))
	}
	engine := NewEngine(ts, Config{StationID: "test-station", StaleAfter: time.Hour})

	fresh := engine.Fresh()

	if !fresh.Stale {
		t.Error("Stale = false with only backfilled history and no live reading")
	}
	if len(fresh.Days) == 0 {
		t.Error("Days is empty: the forecast should still be served, flagged stale")
	}

	// The first live reading clears it.
	ts.Append(obsAtTime(now, 13, 80, 5))
	if engine.Fresh().Stale {
		t.Error("Stale = true after a live reading arrived")
	}
}

func TestFreshWithNoDataIsStaleWithEmptyDays(t *testing.T) {
	engine := NewEngine(store.NewTimeSeries(24), Config{StationID: "test-station", StaleAfter: time.Hour})

	fresh := engine.Fresh()

	if !fresh.Stale {
		t.Error("Stale = false, want true with no station data")
	}
	if fresh.Days == nil || len(fresh.Days) != 0 {
		t.Errorf("Days = %#v, want a non-nil empty slice", fresh.Days)
	}
	body, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"days":[]`) {
		t.Errorf("JSON = %s, want days to serialise as [] rather than null", body)
	}
}

func TestFreshSharesOneRecomputeAcrossConcurrentCallers(t *testing.T) {
	ts := newTimeSeriesWithHistory(t, 72, 13.0, 80, 5.0)
	counting := &countingOM{data: syntheticHourlyData(time.Now(), defaultPastDays, omForecastDaysBuffer)}
	engine := NewEngine(ts, Config{StationID: "test-station", OpenMeteo: counting, StaleAfter: time.Hour})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if fc := engine.Fresh(); fc == nil || len(fc.Days) == 0 {
				t.Error("Fresh() returned no days")
			}
		}()
	}
	wg.Wait()

	if got := counting.calls.Load(); got != 1 {
		t.Errorf("Open-Meteo was fetched %d times for 16 concurrent callers, want 1", got)
	}
}

type countingOM struct {
	data  *openmeteo.HourlyData
	calls atomic.Int32
}

func (c *countingOM) Fetch(pastDays, forecastDays int) (*openmeteo.HourlyData, error) {
	c.calls.Add(1)
	return c.data, nil
}

func newTimeSeriesWithHistory(t *testing.T, hours int, temp, humidity, windSpeed float64) *store.TimeSeries {
	t.Helper()
	ts := store.NewTimeSeries(hours + 10)
	stationHistory(ts, time.Now(), hours, temp, humidity, windSpeed)
	return ts
}
