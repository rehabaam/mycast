package config

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Bounds enforced on numeric settings.
const (
	// MinHistoryDays is the least history the seasonal station model can use:
	// Holt-Winters needs two full daily cycles.
	MinHistoryDays = 2

	// MaxHistoryDays keeps the single hourly getmeasure request under
	// Netatmo's 1024-point limit (30 days is 720 points).
	MaxHistoryDays = 30

	MinFetchIntervalMin = 1
	MaxFetchIntervalMin = 24 * 60
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

	// HTTP server. BindAddr defaults to loopback: the API is unauthenticated,
	// so exposing it to the network has to be a deliberate choice
	// (BIND_ADDR=0.0.0.0).
	BindAddr string
	Port     string

	// Scheduler
	FetchIntervalMin int

	// History to maintain in memory (days)
	HistoryDays int

	// Whether to use Open-Meteo (NWP/ECMWF) as the primary forecast source,
	// with a local bias correction, falling back to the station-only model
	// if unavailable.
	OpenMeteoEnabled bool
}

// Load reads configuration from the environment (after loading ./.env, if
// present). A setting that is present but invalid is an error rather than
// being silently replaced by its default.
func Load() (*Config, error) {
	loadDotEnv(".env")

	tokenFile, err := defaultTokenFile()
	if err != nil {
		return nil, err
	}
	if v := os.Getenv("TOKEN_FILE"); v != "" {
		if tokenFile, err = expandHome(v); err != nil {
			return nil, fmt.Errorf("TOKEN_FILE: %w", err)
		}
	}

	cfg := &Config{
		ClientID:         os.Getenv("NETATMO_CLIENT_ID"),
		ClientSecret:     os.Getenv("NETATMO_CLIENT_SECRET"),
		RedirectURL:      getEnv("NETATMO_REDIRECT_URL", "http://localhost:8080/auth/callback"),
		StationID:        os.Getenv("NETATMO_STATION_ID"),
		OutdoorModuleID:  os.Getenv("NETATMO_OUTDOOR_MODULE_ID"),
		WindModuleID:     os.Getenv("NETATMO_WIND_MODULE_ID"),
		RainModuleID:     os.Getenv("NETATMO_RAIN_MODULE_ID"),
		TokenFile:        tokenFile,
		BindAddr:         getEnv("BIND_ADDR", "127.0.0.1"),
		Port:             getEnv("PORT", "8080"),
		FetchIntervalMin: 30,
		HistoryDays:      7,
		OpenMeteoEnabled: true,
	}

	if cfg.FetchIntervalMin, err = getEnvIntInRange("FETCH_INTERVAL_MIN", cfg.FetchIntervalMin, MinFetchIntervalMin, MaxFetchIntervalMin); err != nil {
		return nil, err
	}
	if cfg.HistoryDays, err = getEnvIntInRange("HISTORY_DAYS", cfg.HistoryDays, MinHistoryDays, MaxHistoryDays); err != nil {
		return nil, err
	}
	if cfg.OpenMeteoEnabled, err = getEnvBool("OPENMETEO_ENABLED", cfg.OpenMeteoEnabled); err != nil {
		return nil, err
	}
	if p, perr := strconv.Atoi(cfg.Port); perr != nil || p < 1 || p > 65535 {
		return nil, fmt.Errorf("PORT: %q is not a valid port (1-65535)", cfg.Port)
	}

	return cfg, nil
}

func defaultTokenFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate home directory for the default token file (set TOKEN_FILE): %w", err)
	}
	return filepath.Join(home, ".mycast", "tokens.json"), nil
}

// expandHome resolves a leading "~" the way a shell would; the environment
// variable is not expanded by one.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
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
		value = parseDotEnvValue(value)
		// Real env vars take precedence over .env values.
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				log.Printf("config: could not set %s: %v", key, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("config: error reading %s: %v", path, err)
	}
}

// parseDotEnvValue unquotes a value. A quoted value is taken verbatim up to
// its closing quote (so it may contain " #"); an unquoted value ends at an
// inline " #" comment.
func parseDotEnvValue(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if q := value[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(value[1:], q); end >= 0 {
			return value[1 : 1+end]
		}
		return strings.Trim(value, `"'`)
	}
	if idx := strings.Index(value, " #"); idx != -1 {
		value = strings.TrimSpace(value[:idx])
	}
	return value
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvIntInRange(key string, def, lo, hi int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%s: %d is out of range (%d-%d)", key, n, lo, hi)
	}
	return n, nil
}

func getEnvBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not a boolean", key, v)
	}
	return b, nil
}
