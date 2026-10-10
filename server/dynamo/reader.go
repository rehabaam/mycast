package dynamo

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
)

// readTimeout bounds each read made on behalf of an HTTP request.
const readTimeout = 5 * time.Second

// Reader serves the API from stored state. It satisfies api.ForecastSource,
// api.CurrentSource and api.ObservationSource, so the same routes that run
// against an in-process engine run against what the last ingest saved.
type Reader struct {
	s          *Store
	staleAfter time.Duration
	now        func() time.Time
}

// Reader returns a Reader that flags the forecast stale once the station has
// delivered no new measurement for staleAfter.
func (s *Store) Reader(staleAfter time.Duration) *Reader {
	return &Reader{s: s, staleAfter: staleAfter, now: time.Now}
}

// Forecast returns the stored forecast with Stale set from the station's own
// reporting history. It never recomputes: a forecast that is old because the
// ingest function stopped says so, rather than being quietly rebuilt from
// data that is just as old.
//
// With nothing stored yet it returns an empty, stale forecast.
func (r *Reader) Forecast(ctx context.Context) (*forecast.Forecast, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	fc, err := r.s.LoadForecast(ctx)
	switch {
	case errors.Is(err, ErrNotFound):
		fc = &forecast.Forecast{Days: []forecast.DayForecast{}}
	case err != nil:
		return nil, err
	}
	if fc.Days == nil {
		fc.Days = []forecast.DayForecast{}
	}

	_, updatedAt, err := r.s.LoadMeta(ctx)
	if err != nil {
		return nil, err
	}
	fc.Stale = len(fc.Days) == 0 || updatedAt.IsZero() || r.now().Sub(updatedAt) > r.staleAfter
	return fc, nil
}

// Latest returns the stored live reading. Any failure reads as "no reading":
// the API has no way to report a cause, and logs carry it.
func (r *Reader) Latest() (*netatmo.Current, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	cur, err := r.s.LoadCurrent(ctx)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Printf("dynamo: load current reading: %v", err)
		}
		return nil, false
	}
	return cur, true
}

// All returns the stored observations, for /debug.
func (r *Reader) All() []netatmo.Observation {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	obs, err := r.s.LoadObservations(ctx)
	if err != nil {
		log.Printf("dynamo: load observations: %v", err)
		return nil
	}
	return obs
}
