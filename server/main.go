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
	"github.com/rehabaam/mycast/ingest"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/store"
)

// main runs mycast as a long-lived process: it keeps the time series in
// memory, polls the station on a ticker and serves the API itself. The same
// pipeline runs as two AWS Lambda functions; see cmd/ and deploy/.
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		log.Fatal("NETATMO_CLIENT_ID and NETATMO_CLIENT_SECRET must be set")
	}
	if cfg.TokenFile == "" {
		log.Fatal("no home directory to keep the OAuth token in: set TOKEN_FILE")
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

	// --- Discover the station (IDs, location, timezone) via a live reading ---
	log.Println("Fetching current station reading...")
	cur, err := client.GetCurrent()
	if err != nil {
		log.Fatalf("Could not reach station: %v", err)
	}
	station := cur.Station
	if cur.OutdoorAvailable {
		log.Printf("Station connected — outdoor %.1f°C, humidity %.0f%%", cur.OutdoorTemp, cur.OutdoorHumidity)
	} else {
		log.Println("Station connected, but the outdoor module is unavailable — no live outdoor reading")
	}

	// --- Load historical data first (ascending timestamps) ---
	// History goes in as history, so it never makes the station look like it
	// is currently reporting (see store.TimeSeries.UpdatedAt).
	ts := store.NewTimeSeries(cfg.HistoryDays * 24)
	log.Printf("Loading %d days of historical data from Netatmo...", cfg.HistoryDays)
	if n, err := ingest.CatchUp(client, station, ts, time.Now(), time.Duration(cfg.HistoryDays)*24*time.Hour); err != nil {
		log.Printf("Warning: history load failed (%v) — forecast will use current reading only", err)
	} else {
		log.Printf("Loaded %d hourly observations", n)
	}

	// The live reading goes in last: it is the newest.
	latest := &store.CurrentCache{}
	latest.Put(cur)
	if obs, ok := cur.Observation(); ok {
		ts.Append(obs)
	}

	// --- Forecast engine ---
	engine := ingest.NewEngine(cfg, station, ts)
	log.Println("Computing initial forecast...")
	engine.Compute()
	log.Println("Forecast ready")

	// --- Background scheduler ---
	go runScheduler(ctx, client, latest, ts, engine, cfg)

	// --- HTTP API ---
	addr := net.JoinHostPort(cfg.BindAddr, cfg.Port)
	srv := api.NewServer(addr, cfg.StaleAfter(), engine, latest, ts)
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

// runScheduler periodically fetches the latest station reading, folds it into
// the time series, and recomputes the forecast.
func runScheduler(ctx context.Context, src ingest.Source, latest *store.CurrentCache, ts *store.TimeSeries, engine *forecast.Engine, cfg *config.Config) {
	ticker := time.NewTicker(time.Duration(cfg.FetchIntervalMin) * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ingest.Tick(src, latest, ts, engine, time.Now())
		}
	}
}
