package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolate runs a test in an empty directory (so no real .env is read) with
// every setting cleared.
func isolate(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, k := range []string{
		"NETATMO_CLIENT_ID", "NETATMO_CLIENT_SECRET", "NETATMO_REDIRECT_URL",
		"NETATMO_STATION_ID", "NETATMO_OUTDOOR_MODULE_ID", "NETATMO_WIND_MODULE_ID", "NETATMO_RAIN_MODULE_ID",
		"TOKEN_FILE", "BIND_ADDR", "PORT", "FETCH_INTERVAL_MIN", "HISTORY_DAYS", "OPENMETEO_ENABLED",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.BindAddr != "127.0.0.1" {
		t.Errorf("BindAddr = %q, want loopback by default", cfg.BindAddr)
	}
	if cfg.Port != "8080" || cfg.FetchIntervalMin != 30 || cfg.HistoryDays != 7 || !cfg.OpenMeteoEnabled {
		t.Errorf("defaults = %+v", cfg)
	}
	if want := filepath.Join(home, ".mycast", "tokens.json"); cfg.TokenFile != want {
		t.Errorf("TokenFile = %q, want %q", cfg.TokenFile, want)
	}
	if cfg.RedirectURL != "http://localhost:8080/auth/callback" {
		t.Errorf("RedirectURL = %q", cfg.RedirectURL)
	}
}

func TestLoadReadsSettings(t *testing.T) {
	isolate(t)
	t.Setenv("BIND_ADDR", "0.0.0.0")
	t.Setenv("PORT", "9090")
	t.Setenv("FETCH_INTERVAL_MIN", "5")
	t.Setenv("HISTORY_DAYS", "14")
	t.Setenv("OPENMETEO_ENABLED", "false")
	t.Setenv("NETATMO_CLIENT_ID", "id")
	t.Setenv("NETATMO_OUTDOOR_MODULE_ID", "02:00")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddr != "0.0.0.0" || cfg.Port != "9090" || cfg.FetchIntervalMin != 5 || cfg.HistoryDays != 14 ||
		cfg.OpenMeteoEnabled || cfg.ClientID != "id" || cfg.OutdoorModuleID != "02:00" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	cases := []struct{ key, val, want string }{
		{"FETCH_INTERVAL_MIN", "0", "out of range"},
		{"FETCH_INTERVAL_MIN", "-5", "out of range"},
		{"FETCH_INTERVAL_MIN", "1441", "out of range"},
		{"FETCH_INTERVAL_MIN", "soon", "not an integer"},
		{"HISTORY_DAYS", "1", "out of range"},
		{"HISTORY_DAYS", "31", "out of range"},
		{"HISTORY_DAYS", "a week", "not an integer"},
		{"PORT", "0", "valid port"},
		{"PORT", "70000", "valid port"},
		{"PORT", "http", "valid port"},
		{"OPENMETEO_ENABLED", "maybe", "not a boolean"},
	}
	for _, c := range cases {
		isolate(t)
		t.Setenv(c.key, c.val)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s=%q: err = %v, want one naming the key and containing %q", c.key, c.val, err, c.want)
		}
	}
}

func TestLoadAcceptsBoundaryValues(t *testing.T) {
	for _, c := range []struct{ key, val string }{
		{"FETCH_INTERVAL_MIN", "1"}, {"FETCH_INTERVAL_MIN", "1440"},
		{"HISTORY_DAYS", "2"}, {"HISTORY_DAYS", "30"},
		{"PORT", "1"}, {"PORT", "65535"},
	} {
		isolate(t)
		t.Setenv(c.key, c.val)
		if _, err := Load(); err != nil {
			t.Errorf("%s=%s: %v", c.key, c.val, err)
		}
	}
}

func TestTokenFileExpandsTilde(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TOKEN_FILE", "~/secrets/t.json")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "secrets", "t.json"); cfg.TokenFile != want {
		t.Errorf("TokenFile = %q, want %q", cfg.TokenFile, want)
	}

	t.Setenv("TOKEN_FILE", "/abs/t.json")
	if cfg, _ = Load(); cfg.TokenFile != "/abs/t.json" {
		t.Errorf("absolute TOKEN_FILE changed to %q", cfg.TokenFile)
	}
}

func TestDotEnvIsLoadedButRealEnvironmentWins(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	dotenv := "# comment\nNETATMO_CLIENT_ID=from-file\nNETATMO_CLIENT_SECRET=\"s3 #cret\"  # trailing\nPORT=9191\n"
	if err := os.WriteFile(".env", []byte(dotenv), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT", "7070") // set for real: must win over the file

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "from-file" {
		t.Errorf("ClientID = %q", cfg.ClientID)
	}
	if cfg.ClientSecret != "s3 #cret" {
		t.Errorf("ClientSecret = %q, want the quoted value including its ' #'", cfg.ClientSecret)
	}
	if cfg.Port != "7070" {
		t.Errorf("Port = %q, want the real environment to win", cfg.Port)
	}
}

func TestParseDotEnvValue(t *testing.T) {
	cases := map[string]string{
		`plain`:              "plain",
		`  spaced  `:         "spaced",
		`value # comment`:    "value",
		`value#nocomment`:    "value#nocomment",
		`"quoted # kept"`:    "quoted # kept",
		`'single # kept'`:    "single # kept",
		`"quoted" # comment`: "quoted",
		`"unterminated`:      "unterminated",
		``:                   "",
		`a=b=c`:              "a=b=c",
	}
	for in, want := range cases {
		if got := parseDotEnvValue(in); got != want {
			t.Errorf("parseDotEnvValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadWithoutAHomeDirectoryLeavesTheTokenFileUnset(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "") // a serverless runtime may have none

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed without a home directory: %v", err)
	}
	if cfg.TokenFile != "" {
		t.Errorf("TokenFile = %q, want empty (the caller decides whether it needs one)", cfg.TokenFile)
	}
}

func TestStaleAfter(t *testing.T) {
	cases := []struct {
		intervalMin int
		want        time.Duration
	}{
		{1, 30 * time.Minute},  // never tighter than the module's reporting cadence allows
		{10, 30 * time.Minute}, // 2x = 20 min, raised to the floor
		{15, 30 * time.Minute}, // 2x lands exactly on the floor
		{30, 60 * time.Minute}, // the default
		{120, 240 * time.Minute},
	}
	for _, c := range cases {
		cfg := &Config{FetchIntervalMin: c.intervalMin}
		if got := cfg.StaleAfter(); got != c.want {
			t.Errorf("FETCH_INTERVAL_MIN=%d: StaleAfter = %v, want %v", c.intervalMin, got, c.want)
		}
	}
}
