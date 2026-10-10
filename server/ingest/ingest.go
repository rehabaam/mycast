// Package ingest is the data pipeline shared by every way of running mycast:
// take a station reading, fold it into the time series, reconcile recent
// hours against Netatmo's own aggregates, and recompute the forecast.
//
// It holds no clock, schedule or storage of its own. A long-running process
// calls Tick from a ticker; a serverless invocation loads its state, calls
// Apply once, and saves what changed.
package ingest

import (
	"log"
	"time"

	"github.com/rehabaam/mycast/config"
	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/noaa"
	"github.com/rehabaam/mycast/openmeteo"
	"github.com/rehabaam/mycast/store"
)

// ReconcileWindow is how far back each tick re-reads Netatmo's hourly
// history. It spans several hours so an hour that Netatmo hadn't aggregated
// yet at one tick is picked up at a later one.
const ReconcileWindow = 3 * time.Hour

// Source is the part of *netatmo.Client the pipeline uses, so it can be
// tested without the network.
type Source interface {
	GetCurrent() (*netatmo.Current, error)
	GetHistory(st netatmo.Station, from, to time.Time) ([]netatmo.Observation, error)
}

// CurrentSink receives every successfully fetched reading, so /current can
// serve it. *store.CurrentCache implements it.
type CurrentSink interface {
	Put(*netatmo.Current)
}

// Outcome reports what a tick did, so a caller can persist exactly that.
type Outcome struct {
	// Current is the fetched reading, or nil if the fetch failed.
	Current *netatmo.Current

	// Appended is true if a live outdoor reading was accepted into the series.
	Appended bool

	// Recomputed is true if the forecast was recomputed.
	Recomputed bool
}

// Tick fetches the latest reading and applies it.
func Tick(src Source, latest CurrentSink, ts *store.TimeSeries, engine *forecast.Engine, now time.Time) Outcome {
	cur, err := src.GetCurrent()
	if err != nil {
		log.Printf("Scheduler: fetch error: %v", err)
		return Outcome{}
	}
	return Apply(src, cur, latest, ts, engine, now)
}

// Apply folds an already-fetched reading into the series: it is Tick without
// the fetch, for callers that needed the reading first (for instance to
// discover the station before building an engine).
func Apply(src Source, cur *netatmo.Current, latest CurrentSink, ts *store.TimeSeries, engine *forecast.Engine, now time.Time) Outcome {
	out := Outcome{Current: cur}

	// /current serves this even when the outdoor module is down: the rest of
	// the reading (indoor, pressure, module health) is still current.
	latest.Put(cur)

	obs, ok := cur.Observation()
	if !ok {
		// Storing the zeroed outdoor fields would record a fake 0 °C / 0 %
		// reading; leaving the gap lets the forecast (and its staleness
		// signal) reflect that the station went quiet.
		log.Println("Scheduler: outdoor module unavailable — skipping this reading")
		return out
	}
	ts.Append(obs)
	out.Appended = true
	reconcileCompletedHours(src, cur.Station, ts, now)

	log.Printf("Scheduler: updated — T=%.1f°C H=%.0f%% W=%.1fkm/h — recomputing forecast", obs.Temperature, obs.Humidity, obs.WindSpeed)
	engine.Compute()
	out.Recomputed = true
	return out
}

// reconcileCompletedHours merges Netatmo's own hourly aggregates into the last
// few *completed* hours. A live reading is only a snapshot: it can't supply a
// clock-hour rain total, and its hourly bucket is provisional until the hour
// is over. The hour still in progress is left to the live reading.
func reconcileCompletedHours(src Source, st netatmo.Station, ts *store.TimeSeries, now time.Time) {
	hist, err := src.GetHistory(st, now.Add(-ReconcileWindow), now)
	if err != nil {
		log.Printf("Scheduler: could not refresh recent hours: %v", err)
		return
	}

	currentHour := store.HourStart(now.Unix())
	completed := make([]netatmo.Observation, 0, len(hist))
	for _, o := range hist {
		if store.HourStart(o.Timestamp) < currentHour {
			completed = append(completed, o)
		}
	}
	ts.Backfill(completed...)
}

// CatchUp loads the history a series is missing, and returns how many hours
// Netatmo supplied. An empty series is filled back to maxLookback; one whose
// newest hour is older than ReconcileWindow (the process was down, or a
// serverless store was empty) is filled from that hour. A series that is
// already current needs nothing: the regular tick reconciles it.
//
// History goes in through Backfill, so it never counts as the station
// reporting (see store.TimeSeries.UpdatedAt).
func CatchUp(src Source, st netatmo.Station, ts *store.TimeSeries, now time.Time, maxLookback time.Duration) (int, error) {
	from := now.Add(-maxLookback)
	if all := ts.All(); len(all) > 0 {
		newest := time.Unix(all[len(all)-1].Timestamp, 0)
		if now.Sub(newest) <= ReconcileWindow {
			return 0, nil
		}
		if newest.After(from) {
			from = newest
		}
	}

	hist, err := src.GetHistory(st, from, now)
	if err != nil {
		return 0, err
	}
	ts.Backfill(hist...)
	return len(hist), nil
}

// NewEngine builds the forecast engine for a station: its timezone decides
// where forecast days end, and Open-Meteo and NOAA are enabled when the
// station's location is known and the integration is switched on. The engine
// falls back to the station-only model by itself if they later fail.
func NewEngine(cfg *config.Config, st netatmo.Station, ts *store.TimeSeries) *forecast.Engine {
	ec := forecast.Config{
		StationID:  st.ID,
		Location:   StationLocation(st.Timezone),
		StaleAfter: cfg.StaleAfter(),
		PastDays:   cfg.HistoryDays,
	}

	if cfg.OpenMeteoEnabled {
		if st.HasLocation {
			ec.OpenMeteo = openmeteo.NewClient(st.Lat, st.Lon)
			ec.Lat, ec.Lon = st.Lat, st.Lon
			// Logged coarsely (~1 km): the exact coordinates are the user's
			// home location and don't belong in logs.
			log.Printf("Open-Meteo enabled near (%.2f, %.2f) — model %s", st.Lat, st.Lon, openmeteo.ModelName)

			// Aurora (NOAA Kp forecast) is a bonus on top of the ECMWF path,
			// enabled alongside it because it needs the same cloud
			// cover/sunrise/sunset data that only that path produces.
			ec.Kp = noaa.NewClient()
			log.Println("Aurora forecasting enabled (NOAA SWPC Kp index)")
		} else {
			log.Println("Open-Meteo disabled: station location unknown")
		}
	}
	return forecast.NewEngine(ts, ec)
}

// StationLocation resolves the station's IANA timezone, falling back to UTC
// (with a warning) when it is unknown or unrecognised.
func StationLocation(name string) *time.Location {
	if name == "" {
		log.Println("Warning: station timezone unknown — forecast days will follow UTC")
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("Warning: unrecognised station timezone %q (%v) — forecast days will follow UTC", name, err)
		return time.UTC
	}
	log.Printf("Forecast days follow the station's timezone, %s", loc)
	return loc
}
