# mycast

A personal weather forecasting service written in Go that fetches live data from your [Netatmo](https://www.netatmo.com) home weather station and produces a 3-day forecast by blending a real NWP model (ECMWF, via [Open-Meteo](https://open-meteo.com/)) with a local bias correction learned from your own station's readings — falling back to a pure station-based statistical model if Open-Meteo is unavailable. See [FORECAST.md](FORECAST.md) for the full technical rationale.

This repo is a monorepo: the Go backend lives in [`server/`](server/) (documented below) and a SwiftUI iOS/macOS client that consumes its API lives in [`app/`](app/).

---

## Features

- Authenticates with the Netatmo API via OAuth2 (browser flow, token persisted to disk)
- Auto-discovers all station modules (outdoor, wind gauge, rain gauge) and the station's location
- Loads 7 days of hourly history and keeps it in an in-memory time-series buffer
- Primary forecast: ECMWF (via Open-Meteo) at the station's exact coordinates, corrected with a local Model-Output-Statistics bias learned from the station's own readings
- Automatic fallback to a deterministic, pure-Go statistical model (damped-trend Holt-Winters) if Open-Meteo is unreachable
- Forecasts the next 72 hours for: temperature, humidity, wind speed, wind direction, precipitation, general condition (WMO code), and apparent ("feels like") temperature; daily sunrise/sunset times
- Aurora visibility estimate per hour, combining NOAA's Kp index forecast with the station's geomagnetic latitude, real cloud cover, and darkness
- Surfaces station health (per-module battery/connectivity), today's actual recorded outdoor min/max, and Netatmo's own pressure/temperature trend on `/current`
- Refreshes data and recomputes every 30 minutes in the background
- Exposes a JSON REST API, including which model produced each forecast

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Go 1.27+ | `go version` to check |
| Netatmo weather station | Main station + outdoor module required; wind gauge and rain gauge optional |
| Netatmo developer account | Free at [dev.netatmo.com](https://dev.netatmo.com) |

---

## Setup

### 1 — Create a Netatmo app

1. Go to [dev.netatmo.com/apps](https://dev.netatmo.com/apps) and sign in.
2. Click **Create** → give it any name.
3. Set **Redirect URI** to `http://localhost:8080/auth/callback`.
4. Copy your **Client ID** and **Client Secret**.

### 2 — Configure environment

```bash
cd server
cp .env.example .env
```

Edit `.env`:

```env
NETATMO_CLIENT_ID=<your client id>
NETATMO_CLIENT_SECRET=<your client secret>
```

All other values are optional — module MAC addresses are auto-discovered on first run. You can fill them in to skip discovery:

```env
NETATMO_STATION_ID=70:ee:50:xx:xx:xx
NETATMO_OUTDOOR_MODULE_ID=02:00:00:xx:xx:xx
NETATMO_WIND_MODULE_ID=06:00:00:xx:xx:xx    # optional
NETATMO_RAIN_MODULE_ID=05:00:00:xx:xx:xx    # optional
```

> **Finding module MACs:** Netatmo app → Settings → My Home → Station, or run the service once and check the logs.

### 3 — Run

```bash
go run .   # from server/
```

On the **first run** the service opens your browser for OAuth2 authorisation. After you approve, the token is saved to `~/.mycast/tokens.json` and all future runs are fully automatic.

```
2026/04/26 16:28:35 Fetching current station reading...
2026/04/26 16:28:36 Station connected — outdoor 8.3°C, humidity 34%
2026/04/26 16:28:36 Loading 7 days of historical data from Netatmo...
2026/04/26 16:28:37 Loaded 168 hourly observations
2026/04/26 16:28:37 Computing initial forecast...
2026/04/26 16:28:38 Forecast ready
2026/04/26 16:28:38 mycast running — forecast available at http://localhost:8080/forecast
```

---

## API

### `GET /forecast`

3-day forecast with daily summaries and hourly breakdowns.

```jsonc
{
  "generated_at": "2026-04-26T16:28:38Z",
  "station_id": "70:ee:50:xx:xx:xx",
  "model": "ecmwf_ifs025+local-bias-correction",
  "stale": false,
  "days": [
    {
      "date": "2026-04-26",
      "day_of_week": "Sunday",
      "sunrise": "2026-04-26T03:57:00Z",
      "sunset": "2026-04-26T19:14:00Z",
      "aurora": {
        "max_probability_pct": 0,
        "hourly": [{ "time": "...", "kp": 1.33, "required_kp": 4.29, "cloud_cover_pct": 21, "is_dark": true, "probability_pct": 0 }, ...]
      },
      "condition": {
        "summary": "Partly cloudy",
        "hourly": [{ "time": "...", "code": 2, "summary": "Partly cloudy" }, ...]
      },
      "temperature": {
        "min_c": 0.92, "max_c": 12.97, "avg_c": 6.3,
        "hourly": [{ "time": "...", "value": 8.73 }, ...],
        "apparent_avg_c": 4.1,
        "apparent_hourly": [{ "time": "...", "value": 6.9 }, ...]
      },
      "humidity": {
        "min_pct": 28.0, "max_pct": 55.0, "avg_pct": 38.4,
        "hourly": [...]
      },
      "wind": {
        "avg_speed_kmh": 5.26, "max_speed_kmh": 12.0,
        "avg_direction_deg": 337.5, "cardinal": "NNW",
        "hourly": [{ "time": "...", "speed_kmh": 4.0, "direction_deg": 330.0, "cardinal": "NNW" }, ...]
      },
      "precipitation": {
        "total_mm": 0.0, "probability": 0.11,
        "hourly": [{ "time": "...", "amount_mm": 0.0, "probability": 0.09 }, ...]
      }
    },
    // day 2 …
    // day 3 …
  ]
}
```

> `condition`, `sunrise`/`sunset`, and `aurora` are only available on the ECMWF path — on the station-only fallback, `condition.summary` reads `"Unknown"` and `sunrise`/`sunset`/`aurora` are omitted entirely, since none of them has a local source. `apparent_*` ("feels like") is computed on both paths from temperature + humidity + wind speed, so it's always populated.
>
> `aurora` is a deliberately simple heuristic, not a validated aurora nowcast: it checks whether NOAA's forecast Kp index clears the threshold for your station's geomagnetic latitude, then derates for daylight and cloud cover. It doesn't model solar wind speed or IMF orientation, so treat it as "is it geomagnetically active enough, with clear dark sky" rather than a precise prediction. Requires `OPENMETEO_ENABLED=true` (aurora needs the same cloud cover/sunrise/sunset data as the ECMWF path).
>
> `/forecast` always returns a live-checked forecast: if the cached one is older than 2×`FETCH_INTERVAL_MIN` (e.g. the background scheduler stalled), the server recomputes synchronously before responding rather than silently serving stale day labels. `stale` is `true` only if that recompute still couldn't produce a current forecast (e.g. no station data has ever arrived).

### `GET /current`

Live station readings fetched directly from Netatmo.

```jsonc
{
  "Timestamp": 1777209201,
  "OutdoorTemp": 8.3,
  "OutdoorHumidity": 34,
  "ApparentTempC": 5.9,
  "IndoorTemp": 22.2,
  "IndoorHumidity": 36,
  "Pressure": 1004.1,
  "PressureTrend": "up",
  "TempTrend": "stable",
  "WindSpeed": 3,
  "WindAngle": 345,
  "GustSpeed": 9,
  "GustAngle": 23,
  "Rain": 0,
  "SumRain1h": 0,
  "SumRain24h": 0.1,
  "TodayOutdoorMinC": 6.1,
  "TodayOutdoorMaxC": 9.8,
  "TodayOutdoorMinAt": 1777181201,
  "TodayOutdoorMaxAt": 1777202801,
  "Modules": [
    { "Type": "NAModule1", "Name": "Outdoor", "BatteryPercent": 83, "Reachable": true, "RFStatus": 80, "LastSeen": 1777209195 },
    { "Type": "NAModule2", "Name": "Smart Anemometer", "BatteryPercent": 52, "Reachable": true, "RFStatus": 73, "LastSeen": 1777209201 }
  ]
}
```

### `GET /health`

Liveness check.

```json
{ "status": "ok", "time": "2026-04-26T16:28:38Z" }
```

### `GET /debug`

Inspection endpoint: shows the number of observations in the in-memory store and recent temperature samples. Useful for diagnosing data-loading issues.

---

## Configuration reference

All settings are read from `.env` (loaded automatically) or from real environment variables (which take precedence).

| Variable | Default | Description |
|---|---|---|
| `NETATMO_CLIENT_ID` | _(required)_ | Netatmo OAuth2 client ID |
| `NETATMO_CLIENT_SECRET` | _(required)_ | Netatmo OAuth2 client secret |
| `NETATMO_REDIRECT_URL` | `http://localhost:8080/auth/callback` | Must match Netatmo app settings |
| `NETATMO_STATION_ID` | _(auto)_ | Main station MAC address |
| `NETATMO_OUTDOOR_MODULE_ID` | _(auto)_ | Outdoor module MAC (NAModule1) |
| `NETATMO_WIND_MODULE_ID` | _(auto)_ | Wind gauge MAC (NAModule2) |
| `NETATMO_RAIN_MODULE_ID` | _(auto)_ | Rain gauge MAC (NAModule3) |
| `TOKEN_FILE` | `~/.mycast/tokens.json` | OAuth2 token persistence path |
| `PORT` | `8080` | HTTP server port |
| `FETCH_INTERVAL_MIN` | `30` | How often to poll the station (minutes) |
| `HISTORY_DAYS` | `7` | Days of hourly history to load and retain |
| `OPENMETEO_ENABLED` | `true` | Use ECMWF (via Open-Meteo) with local bias correction as the primary forecast; falls back to the station-only model automatically if unreachable. No API key needed. |

---

## Project layout

```
server/
├── main.go                    # startup: auth → history → scheduler → HTTP server
├── config/config.go           # .env loader + typed config struct
├── netatmo/
│   ├── auth.go                # OAuth2 authorization-code flow + token file
│   ├── client.go              # getstationsdata + getmeasure API client
│   └── models.go              # Netatmo JSON types + Observation / Current structs
├── openmeteo/client.go        # Open-Meteo (ECMWF) forecast client
├── noaa/client.go             # NOAA SWPC Kp index forecast client (aurora)
├── store/timeseries.go        # thread-safe in-memory circular buffer
├── forecast/
│   ├── holtwinters.go         # deterministic damped-trend Holt-Winters (station-only fallback)
│   ├── mos.go                 # local bias correction (Model Output Statistics) for the ECMWF path
│   ├── aurora.go              # geomagnetic latitude + Kp-based aurora visibility heuristic
│   ├── engine.go              # ECMWF+MOS path with station-only fallback, caches forecast
│   ├── precipitation.go       # persistence + climatological precipitation model
│   ├── wind.go                # circular mean for wind direction aggregation
│   └── models.go              # Forecast / DayForecast JSON response types
└── api/
    ├── server.go              # HTTP server + middleware
    └── handlers.go            # /forecast  /current  /health  /debug
```

`app/` is a SwiftUI client (Xcode project) that consumes this API — see `app/MyCast/Networking/WeatherService.swift` for the request layer.

---

## Dependencies

| Package | Purpose |
|---|---|
| `golang.org/x/oauth2` | Netatmo OAuth2 authentication |
| Standard library only | All other functionality |

External services: [Netatmo API](https://dev.netatmo.com/) (station data, requires an account), [Open-Meteo](https://open-meteo.com/) (ECMWF forecast, free, no key — set `OPENMETEO_ENABLED=false` to disable and run station-only), and [NOAA SWPC](https://www.swpc.noaa.gov/) (Kp index forecast for aurora, free, no key — automatically enabled alongside Open-Meteo).
