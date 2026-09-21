package config

import (
	"bufio"
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// Netatmo OAuth2
	ClientID     string
	ClientSecret string
	RedirectURL  string

	// Station module MAC addresses
	StationID       string
	OutdoorModuleID string
	WindModuleID    string
	RainModuleID    string

	// Token storage path
	TokenFile string

	// HTTP server
	Port string

	// Scheduler
	FetchIntervalMin int

	// History to maintain in memory (days)
	HistoryDays int

	// Whether to use Open-Meteo (NWP/ECMWF) as the primary forecast source,
	// with a local bias correction, falling back to the station-only model
	// if unavailable.
	OpenMeteoEnabled bool
}

func Load() *Config {
	loadDotEnv(".env")

	return &Config{
		ClientID:         getEnv("NETATMO_CLIENT_ID", ""),
		ClientSecret:     getEnv("NETATMO_CLIENT_SECRET", ""),
		RedirectURL:      getEnv("NETATMO_REDIRECT_URL", "http://localhost:8080/auth/callback"),
		StationID:        getEnv("NETATMO_STATION_ID", ""),
		OutdoorModuleID:  getEnv("NETATMO_OUTDOOR_MODULE_ID", ""),
		WindModuleID:     getEnv("NETATMO_WIND_MODULE_ID", ""),
		RainModuleID:     getEnv("NETATMO_RAIN_MODULE_ID", ""),
		TokenFile:        getEnv("TOKEN_FILE", os.ExpandEnv("$HOME/.mycast/tokens.json")),
		Port:             getEnv("PORT", "8080"),
		FetchIntervalMin: getEnvInt("FETCH_INTERVAL_MIN", 30),
		HistoryDays:      getEnvInt("HISTORY_DAYS", 7),
		OpenMeteoEnabled: getEnvBool("OPENMETEO_ENABLED", true),
	}
}

// loadDotEnv reads key=value pairs from path and sets them as environment
// variables, skipping keys that are already set in the environment.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // .env is optional
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// Strip inline comments and surrounding quotes.
		if idx := strings.Index(value, " #"); idx != -1 {
			value = strings.TrimSpace(value[:idx])
		}
		value = strings.Trim(value, `"'`)
		// Real env vars take precedence over .env values.
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				log.Printf("config: could not set %s: %v", key, err)
			}
		}
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
