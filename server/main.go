package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed the timezone database, so station timezones resolve on minimal hosts

	"github.com/rehabaam/mycast/api"
	"github.com/rehabaam/mycast/config"
	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/noaa"
	"github.com/rehabaam/mycast/openmeteo"
	"github.com/rehabaam/mycast/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		log.Fatal("NETATMO_CLIENT_ID and NETATMO_CLIENT_SECRET must be set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Authentication ---
	auth := netatmo.NewAuthenticator(cfg.ClientID, cfg.ClientSecret, cfg.RedirectURL, cfg.TokenFile)
	httpClient, err := auth.GetHTTPClient(ctx)
	if err != nil {
		log.Fatalf("Authentication failed: %v", err)
	}

	// --- Netatmo client ---
	client := netatmo.NewClient(httpClient, netatmo.Config{
		StationID:       cfg.StationID,
		OutdoorModuleID: cfg.OutdoorModuleID,
		WindModuleID:    cfg.WindModuleID,
		RainModuleID:    cfg.RainModuleID,
	})

	// --- Time series store ---
	ts := store.NewTimeSeries(cfg.HistoryDays * 24)

	// --- Discover the station (IDs, location, timezone) via a live reading ---
	log.Println("Fetching current station reading...")
	cur, err := client.GetCurrent()
	if err != nil {
		log.Fatalf("Could not reach station: %v", err)
	}
	station := cur.Station
	latest := &store.CurrentCache{}
	latest.Put(cur)
	if cur.OutdoorAvailable {
		log.Printf("Station connected — outdoor %.1f°C, humidity %.0f%%", cur.OutdoorTemp, cur.OutdoorHumidity)
	} else {
		log.Println("Station connected, but the outdoor module is unavailable — no live outdoor reading")
	}

	// --- Load historical data first (ascending timestamps) ---
	// The current reading must be appended AFTER history so the time-ordered
	// buffer does not reject older records.
	log.Printf("Loading %d days of historical data from Netatmo...", cfg.HistoryDays)
	from := time.Now().Add(-time.Duration(cfg.HistoryDays) * 24 * time.Hour)
	hist, err := client.GetHistory(station, from, time.Now())
	if err != nil {
		log.Printf("Warning: history load failed (%v) — forecast will use current reading only", err)
	} else {
		// History is not a live report: it must not make the station look
		// like it is currently reporting (see store.TimeSeries.UpdatedAt).
		ts.Backfill(hist...)
		log.Printf("Loaded %d hourly observations", len(hist))
	}

	// Append the live reading last — it has the newest timestamp.
	if obs, ok := cur.Observation(); ok {
		ts.Append(obs)
	}

	// --- Forecast engine ---
	// A station that has delivered no new measurement for staleAfter is
	// considered stale — enough slack that a single missed tick doesn't trip
	// it, but short enough that a stalled scheduler, repeated upstream
	// failures, or an outdoor module that has gone quiet get flagged. The same
	// window bounds how old a cached forecast may get before the next read
	// recomputes it.
	//
	// Staleness follows the module's own measurement time, and Netatmo
	// modules only report every ~10 minutes, so it can't be tighter than a
	// few reporting periods however often the service polls.
	staleAfter := max(2*time.Duration(cfg.FetchIntervalMin)*time.Minute, minStaleAfter)

	engineCfg := forecast.Config{
		StationID:  station.ID,
		Location:   stationLocation(station.Timezone),
		StaleAfter: staleAfter,
		PastDays:   cfg.HistoryDays,
	}

	// NWP forecast source (Open-Meteo/ECMWF), if the station's location is
	// known and the integration is enabled. The engine falls back to the
	// pure station model automatically if it is unset or later fails.
	if cfg.OpenMeteoEnabled {
		if station.HasLocation {
			lat, lon := station.Lat, station.Lon
			engineCfg.OpenMeteo = openmeteo.NewClient(lat, lon)
			engineCfg.Lat, engineCfg.Lon = lat, lon
			// Logged coarsely (~1 km): the exact coordinates are the user's
			// home location and don't belong in logs.
			log.Printf("Open-Meteo enabled near (%.2f, %.2f) — model %s", lat, lon, openmeteo.ModelName)

			// Aurora (NOAA Kp forecast) is a bonus on top of the ECMWF path,
			// enabled alongside it because it needs the same cloud
			// cover/sunrise/sunset data that only that path produces.
			engineCfg.Kp = noaa.NewClient()
			log.Println("Aurora forecasting enabled (NOAA SWPC Kp index)")
		} else {
			log.Println("Open-Meteo disabled: station location unknown")
		}
	}
	engine := forecast.NewEngine(ts, engineCfg)

	log.Println("Computing initial forecast...")
	engine.Compute()
	log.Println("Forecast ready")

	// --- Background scheduler ---
	go runScheduler(ctx, client, latest, ts, engine, cfg)

	// --- HTTP API ---
	addr := net.JoinHostPort(cfg.BindAddr, cfg.Port)
	srv := api.NewServer(addr, staleAfter, engine, latest, ts)
	ln, err := srv.Listen()
	if err != nil {
		log.Fatalf("Cannot listen on %s: %v", addr, err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	log.Printf("mycast running — forecast available at http://%s/forecast", ln.Addr())

	// Run until SIGINT/SIGTERM, or until the server dies on its own.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		log.Println("Shutting down...")
	case err := <-serveErr:
		log.Fatalf("Server stopped unexpectedly: %v", err)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			log.Printf("Server shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		log.Println("Server did not stop in time")
	}
}

// stationLocation resolves the station's IANA timezone, falling back to UTC
// (with a warning) when it is unknown or unrecognised.
func stationLocation(name string) *time.Location {
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

// runScheduler periodically fetches the latest station reading, appends it to
// the time series, and recomputes the forecast.
func runScheduler(ctx context.Context, src stationSource, latest *store.CurrentCache, ts *store.TimeSeries, engine *forecast.Engine, cfg *config.Config) {
	interval := time.Duration(cfg.FetchIntervalMin) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetchAndUpdate(src, latest, ts, engine, time.Now())
		}
	}
}

// minStaleAfter is the shortest staleness window used regardless of the
// polling interval; see main.
const minStaleAfter = 30 * time.Minute

// stationSource is the part of *netatmo.Client the scheduler uses, so a tick
// can be tested without the network.
type stationSource interface {
	GetCurrent() (*netatmo.Current, error)
	GetHistory(st netatmo.Station, from, to time.Time) ([]netatmo.Observation, error)
}

// reconcileWindow is how far back each tick re-reads Netatmo's hourly
// history. It spans several hours so an hour that Netatmo hadn't aggregated
// yet at one tick is picked up at a later one.
const reconcileWindow = 3 * time.Hour

func fetchAndUpdate(src stationSource, latest *store.CurrentCache, ts *store.TimeSeries, engine *forecast.Engine, now time.Time) {
	cur, err := src.GetCurrent()
	if err != nil {
		log.Printf("Scheduler: fetch error: %v", err)
		return
	}
	// /current serves this even when the outdoor module is down: the rest of
	// the reading (indoor, pressure, module health) is still current.
	latest.Put(cur)

	obs, ok := cur.Observation()
	if !ok {
		// Storing the zeroed outdoor fields would record a fake 0 °C / 0 %
		// reading; leaving the gap lets the forecast (and its staleness
		// signal) reflect that the station went quiet.
		log.Println("Scheduler: outdoor module unavailable — skipping this reading")
		return
	}
	ts.Append(obs)
	reconcileCompletedHours(src, cur.Station, ts, now)

	log.Printf("Scheduler: updated — T=%.1f°C H=%.0f%% W=%.1fkm/h — recomputing forecast", obs.Temperature, obs.Humidity, obs.WindSpeed)
	engine.Compute()
}

// reconcileCompletedHours replaces the stored values for the last few
// *completed* hours with Netatmo's own hourly aggregates. A live reading is
// only a snapshot: it can't supply a clock-hour rain total, and its hourly
// bucket is provisional until the hour is over. The hour still in progress is
// left to the live reading.
func reconcileCompletedHours(src stationSource, st netatmo.Station, ts *store.TimeSeries, now time.Time) {
	hist, err := src.GetHistory(st, now.Add(-reconcileWindow), now)
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
