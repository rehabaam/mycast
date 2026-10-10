// Package serverless holds the logic of mycast's AWS deployment that is worth
// testing without AWS: one stateless ingest run, and secret loading. The
// entry points in cmd/ are thin glue around it.
package serverless

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/rehabaam/mycast/config"
	"github.com/rehabaam/mycast/dynamo"
	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/ingest"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/store"
)

// Repository is the persistence one ingest run needs. *dynamo.Store
// implements it.
type Repository interface {
	LoadState(ctx context.Context) (dynamo.State, error)
	SaveObservations(ctx context.Context, obs []netatmo.Observation) error
	SaveMeta(ctx context.Context, lastMeasured int64, updatedAt time.Time) error
	SaveCurrent(ctx context.Context, cur *netatmo.Current) error
	SaveForecast(ctx context.Context, fc *forecast.Forecast) error
}

// Ingestor runs the data pipeline once, with no memory of its own: it loads
// the state the previous run saved, does what a scheduler tick does, and saves
// whatever changed.
type Ingestor struct {
	Repo   Repository
	Source ingest.Source
	Config *config.Config

	// Now and NewEngine are replaceable in tests.
	Now       func() time.Time
	NewEngine func(*config.Config, netatmo.Station, *store.TimeSeries) *forecast.Engine
}

// Result summarises a run, for logging and tests.
type Result struct {
	ObservationsSaved int
	CurrentSaved      bool
	MetaSaved         bool
	ForecastSaved     bool
	CaughtUp          int
}

// Run performs one ingest. An error means nothing about the run should be
// trusted; it is returned so the invocation is recorded as failed.
func (g *Ingestor) Run(ctx context.Context) (Result, error) {
	var res Result
	now := g.Now()
	newEngine := g.NewEngine
	if newEngine == nil {
		newEngine = ingest.NewEngine
	}

	state, err := g.Repo.LoadState(ctx)
	if err != nil {
		return res, fmt.Errorf("load state: %w", err)
	}
	ts := store.NewTimeSeries(g.Config.HistoryDays * 24)
	ts.Restore(state.Observations, state.LastMeasured, state.UpdatedAt)
	before := ts.All()
	beforeMeasured, beforeUpdated := ts.LastMeasured(), ts.UpdatedAt()

	cur, err := g.Source.GetCurrent()
	if err != nil {
		return res, fmt.Errorf("fetch current reading: %w", err)
	}

	// An empty store, or one the function has not touched for a while, is
	// filled from Netatmo's history before the live reading is applied.
	lookback := time.Duration(g.Config.HistoryDays) * 24 * time.Hour
	n, err := ingest.CatchUp(g.Source, cur.Station, ts, now, lookback)
	if err != nil {
		log.Printf("serverless: history catch-up failed (%v) — continuing with what is stored", err)
	}
	res.CaughtUp = n

	engine := newEngine(g.Config, cur.Station, ts)
	out := ingest.Apply(g.Source, cur, &store.CurrentCache{}, ts, engine, now)

	// Save what changed. The reading first (it is what /current serves), then
	// the data, then the forecast derived from it: if the run dies part-way,
	// the forecast is merely older than the data, never newer.
	if err := g.Repo.SaveCurrent(ctx, cur); err != nil {
		return res, fmt.Errorf("save current reading: %w", err)
	}
	res.CurrentSaved = true

	changed := changedObservations(before, ts.All())
	if len(changed) > 0 {
		if err := g.Repo.SaveObservations(ctx, changed); err != nil {
			return res, fmt.Errorf("save observations: %w", err)
		}
		res.ObservationsSaved = len(changed)
	}

	if ts.LastMeasured() != beforeMeasured || !ts.UpdatedAt().Equal(beforeUpdated) {
		if err := g.Repo.SaveMeta(ctx, ts.LastMeasured(), ts.UpdatedAt()); err != nil {
			return res, fmt.Errorf("save staleness state: %w", err)
		}
		res.MetaSaved = true
	}

	if out.Recomputed {
		fc := engine.Latest()
		if fc == nil {
			return res, errors.New("forecast was recomputed but is not available")
		}
		if err := g.Repo.SaveForecast(ctx, fc); err != nil {
			return res, fmt.Errorf("save forecast: %w", err)
		}
		res.ForecastSaved = true
	}
	return res, nil
}

// changedObservations returns the observations in after that are new or
// different from before, so a run writes a handful of items rather than the
// whole window.
func changedObservations(before, after []netatmo.Observation) []netatmo.Observation {
	prev := make(map[int64]netatmo.Observation, len(before))
	for _, o := range before {
		prev[o.Timestamp] = o
	}
	var out []netatmo.Observation
	for _, o := range after {
		if p, ok := prev[o.Timestamp]; !ok || p != o {
			out = append(out, o)
		}
	}
	return out
}
