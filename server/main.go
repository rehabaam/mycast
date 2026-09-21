package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rehabaam/mycast/api"
	"github.com/rehabaam/mycast/config"
	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/noaa"
	"github.com/rehabaam/mycast/openmeteo"
	"github.com/rehabaam/mycast/store"
)

func main() {
	cfg := config.Load()

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
	maxHours := cfg.HistoryDays * 24
	ts := store.NewTimeSeries(maxHours)

	// --- Discover station/module IDs via a live reading ---
	log.Println("Fetching current station reading...")
	cur, err := client.GetCurrent()
	if err != nil {
		log.Fatalf("Could not reach station: %v", err)
	}
	log.Printf("Station connected — outdoor %.1f°C, humidity %.0f%%", cur.OutdoorTemp, cur.OutdoorHumidity)

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
	ts.Append(netatmo.Observation{
		Timestamp:   cur.Timestamp,
		Temperature: cur.OutdoorTemp,
		Humidity:    cur.OutdoorHumidity,
		WindSpeed:   cur.WindSpeed,
		WindAngle:   cur.WindAngle,
		GustSpeed:   cur.GustSpeed,
		GustAngle:   cur.GustAngle,
		Rain:        cur.Rain,
	})

	// --- NWP forecast source (Open-Meteo/ECMWF), if the station's location
	// is known and the integration is enabled. The engine falls back to the
	// pure station model automatically if this is nil or later fails.
	var om *openmeteo.Client
	var lat, lon float64
	if cfg.OpenMeteoEnabled {
		if la, lo, ok := client.Location(); ok {
			lat, lon = la, lo
			om = openmeteo.NewClient(lat, lon)
			log.Printf("Open-Meteo enabled for (%.4f, %.4f) — model %s", lat, lon, openmeteo.ModelName)
		} else {
			log.Println("Open-Meteo disabled: station location unknown")
		}
	}

	// --- Aurora (NOAA Kp forecast) — a bonus on top of the ECMWF path.
	// Always enabled when Open-Meteo is, since it needs the same cloud
	// cover/sunrise/sunset data that only the ECMWF path produces.
	var kp *noaa.Client
	if om != nil {
		kp = noaa.NewClient()
		log.Println("Aurora forecasting enabled (NOAA SWPC Kp index)")
	}

	// --- Forecast engine ---
	// A forecast older than 2 fetch intervals is considered stale — enough
	// slack that a single missed tick doesn't trip it, but short enough that
	// a stalled scheduler (e.g. this process being paused, or repeated
	// upstream failures) gets caught and self-healed on the next read.
	staleAfter := 2 * time.Duration(cfg.FetchIntervalMin) * time.Minute

	// om/kp passed explicitly rather than storing possibly-nil concrete
	// pointers directly: a nil pointer wrapped in a non-nil interface value
	// would defeat the engine's internal `!= nil` fallback checks. kp is
	// only ever set alongside om (see above), so these are the only two
	// reachable states.
	var engine *forecast.Engine
	if om != nil {
		engine = forecast.NewEngine(ts, cfg.StationID, om, kp, lat, lon, staleAfter)
	} else {
		engine = forecast.NewEngine(ts, cfg.StationID, nil, nil, 0, 0, staleAfter)
	}

	log.Println("Computing initial forecast...")
	engine.Compute()
	log.Println("Forecast ready")

	// --- Background scheduler ---
	go runScheduler(ctx, client, ts, engine, cfg)

	// --- HTTP API ---
	srv := api.NewServer(cfg.Port, engine, client, ts)
	go func() {
		if err := srv.Start(ctx); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	log.Printf("mycast running — forecast available at http://localhost:%s/forecast", cfg.Port)

	// Graceful shutdown on SIGINT/SIGTERM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
	cancel()
	time.Sleep(500 * time.Millisecond)
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

	obs := netatmo.Observation{
		Timestamp:   cur.Timestamp,
		Temperature: cur.OutdoorTemp,
		Humidity:    cur.OutdoorHumidity,
		WindSpeed:   cur.WindSpeed,
		WindAngle:   cur.WindAngle,
		GustSpeed:   cur.GustSpeed,
		GustAngle:   cur.GustAngle,
		Rain:        cur.Rain,
	}
	ts.Append(obs)

	log.Printf("Scheduler: updated — T=%.1f°C H=%.0f%% W=%.1fkm/h — recomputing forecast", obs.Temperature, obs.Humidity, obs.WindSpeed)
	engine.Compute()
}
