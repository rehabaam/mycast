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
	client := netatmo.NewClient(httpClient, cfg.StationID, cfg.OutdoorModuleID, cfg.WindModuleID, cfg.RainModuleID)

	// --- Time series store ---
	ts := store.NewTimeSeries(cfg.HistoryDays * 24)

	// --- Discover station/module IDs via a live reading ---
	log.Println("Fetching current station reading...")
	cur, err := client.GetCurrent()
	if err != nil {
		log.Fatalf("Could not reach station: %v", err)
	}
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
	hist, err := client.GetHistory(from, time.Now())
	if err != nil {
		log.Printf("Warning: history load failed (%v) — forecast will use current reading only", err)
	} else {
		ts.Append(hist...)
		log.Printf("Loaded %d hourly observations", len(hist))
	}

	// Append the live reading last — it has the newest timestamp.
	if obs, ok := cur.Observation(); ok {
		ts.Append(obs)
	}

	// --- Forecast engine ---
	// A station that has been silent for more than 2 fetch intervals is
	// considered stale — enough slack that a single missed tick doesn't trip
	// it, but short enough that a stalled scheduler (e.g. this process being
	// paused, repeated upstream failures, or an offline outdoor module) gets
	// flagged. The same window bounds how old a cached forecast may get
	// before the next read recomputes it.
	staleAfter := 2 * time.Duration(cfg.FetchIntervalMin) * time.Minute

	engineCfg := forecast.Config{
		StationID:  client.StationID(),
		Location:   stationLocation(client.Timezone()),
		StaleAfter: staleAfter,
		PastDays:   cfg.HistoryDays,
	}

	// NWP forecast source (Open-Meteo/ECMWF), if the station's location is
	// known and the integration is enabled. The engine falls back to the
	// pure station model automatically if it is unset or later fails.
	if cfg.OpenMeteoEnabled {
		if lat, lon, ok := client.Location(); ok {
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
	go runScheduler(ctx, client, ts, engine, cfg)

	// --- HTTP API ---
	addr := net.JoinHostPort(cfg.BindAddr, cfg.Port)
	srv := api.NewServer(addr, engine, client, ts)
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
func runScheduler(ctx context.Context, client *netatmo.Client, ts *store.TimeSeries, engine *forecast.Engine, cfg *config.Config) {
	interval := time.Duration(cfg.FetchIntervalMin) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetchAndUpdate(client, ts, engine)
		}
	}
}

func fetchAndUpdate(client *netatmo.Client, ts *store.TimeSeries, engine *forecast.Engine) {
	cur, err := client.GetCurrent()
	if err != nil {
		log.Printf("Scheduler: fetch error: %v", err)
		return
	}

	obs, ok := cur.Observation()
	if !ok {
		// Storing the zeroed outdoor fields would record a fake 0 °C / 0 %
		// reading; leaving the gap lets the forecast (and its staleness
		// signal) reflect that the station went quiet.
		log.Println("Scheduler: outdoor module unavailable — skipping this reading")
		return
	}
	ts.Append(obs)

	log.Printf("Scheduler: updated — T=%.1f°C H=%.0f%% W=%.1fkm/h — recomputing forecast", obs.Temperature, obs.Humidity, obs.WindSpeed)
	engine.Compute()
}
