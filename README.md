# mycast

A personal weather forecasting service written in Go that fetches live data from your [Netatmo](https://www.netatmo.com) home weather station and produces a 3-day forecast by blending a real NWP model (ECMWF, via [Open-Meteo](https://open-meteo.com/)) with a local bias correction learned from your own station's readings — falling back to a pure station-based statistical model if Open-Meteo is unavailable. See [FORECAST.md](FORECAST.md) for the full technical rationale.

This repo is a monorepo: the Go backend lives in [`server/`](server/) (documented below) and a SwiftUI iOS/macOS client that consumes its API lives in [`app/`](app/).

The backend runs two ways: as a single process on your machine (this page), or as two AWS Lambda functions over a DynamoDB table for about 3 cents a month — see [deploy/README.md](deploy/README.md), which also covers deploying from GitHub Actions with OIDC (no stored AWS keys). [ARCHITECTURE.md](ARCHITECTURE.md) has diagrams of both.

---

## Features

- Authenticates with the Netatmo API via OAuth2 (browser flow, token persisted to disk)
- Auto-discovers all station modules (outdoor, wind gauge, rain gauge) and the station's location
- Loads 7 days of hourly history and keeps it in an in-memory time-series buffer
- Primary forecast: ECMWF (via Open-Meteo) at the station's exact coordinates, corrected with a local Model-Output-Statistics bias learned from the station's own readings
- Automatic fallback to a deterministic, pure-Go statistical model (damped-trend Holt-Winters) if Open-Meteo is unreachable
- Forecasts the next 72 hours, reported as up to three days of the station's *local* calendar (today may only cover its remaining hours), for: temperature, humidity, wind speed, wind direction, precipitation, general condition (WMO code), and apparent ("feels like") temperature; daily sunrise/sunset times
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
3. Set **Redirect URI** to `http://localhost:8080/auth/callback` (the default; it only has to match `NETATMO_REDIRECT_URL`).
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

On the **first run** the service prints an authorisation URL; open it in your browser and approve. The service listens for the redirect on the host and port of `NETATMO_REDIRECT_URL` (loopback only, unless that URL points elsewhere), independent of `PORT`, and ignores any request that doesn't carry the one-time `state` it generated. The token is then saved to `~/.mycast/tokens.json` (mode `0600`) and kept up to date as it refreshes, so all future runs are fully automatic.

```
2026/04/26 16:28:35 Fetching current station reading...
2026/04/26 16:28:36 Station connected — outdoor 8.3°C, humidity 34%
2026/04/26 16:28:36 Loading 7 days of historical data from Netatmo...
2026/04/26 16:28:37 Loaded 168 hourly observations
2026/04/26 16:28:37 Forecast days follow the station's timezone, Europe/Helsinki
2026/04/26 16:28:37 Open-Meteo enabled near (60.17, 24.94) — model ecmwf_ifs025
2026/04/26 16:28:37 Aurora forecasting enabled (NOAA SWPC Kp index)
2026/04/26 16:28:37 Computing initial forecast...
2026/04/26 16:28:38 Forecast ready
2026/04/26 16:28:38 mycast running — forecast available at http://127.0.0.1:8080/forecast
```

> **Network exposure.** The API has no authentication, so it binds to `127.0.0.1` by default and is reachable only from the machine it runs on (which is what the iOS Simulator needs). To use it from a phone or another computer, set `BIND_ADDR=0.0.0.0`, and only on a network you trust.
>
> If the port is already in use the service exits with an error rather than carrying on without a server.

---

## API

### `GET /forecast`

Forecast for up to three days, with daily summaries and hourly breakdowns. A "day" is a calendar day in the station's own timezone (taken from Netatmo): `days[0]` is today and only covers the hours still to come, so late in the evening it may hold a single hour, followed by tomorrow and the day after. `condition.summary` is the condition around local midday (or the first hour, for a day already past noon).

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
> `/forecast` always returns a live-checked forecast: if the cached one is older than 2×`FETCH_INTERVAL_MIN` (e.g. the background scheduler stalled), the server recomputes synchronously before responding rather than silently serving stale day labels. `stale` reports something a recompute can't fix: it is `true` when the station hasn't delivered a *new measurement* for longer than the staleness window (the scheduler is stuck, fetches are failing, or the outdoor module has gone quiet — including while the indoor base keeps reporting) or when there is no data at all, in which case `days` is an empty array. Clients should show a warning, not hide the forecast.
>
> The staleness window is 2×`FETCH_INTERVAL_MIN`, but never less than 30 minutes: it follows the outdoor module's own measurement time, and Netatmo modules only report every ~10 minutes, so polling more often than that mustn't make a healthy station look stale.
>
> Every timestamp is RFC 3339 in UTC, to whole seconds.

### `GET /current`

The station's latest reading, as of the last scheduled fetch (every `FETCH_INTERVAL_MIN`). It is served from memory: requests never trigger a call to Netatmo. Keys are snake_case with units in the name, and timestamps are RFC 3339.

```jsonc
{
  "timestamp": "2026-04-26T16:33:21Z",   // when the indoor base station measured its part
  "fetched_at": "2026-04-26T16:33:40Z",  // when this service last retrieved it
  "outdoor_timestamp": "2026-04-26T16:30:02Z", // when the outdoor module itself measured; omitted if unavailable
  "stale": false,
  "outdoor_available": true,
  "outdoor_temp_c": 8.3,
  "outdoor_humidity_pct": 34,
  "apparent_temp_c": 5.9,
  "indoor_temp_c": 22.2,
  "indoor_humidity_pct": 36,
  "pressure_hpa": 1004.1,
  "pressure_trend": "up",
  "temp_trend": "stable",
  "wind_speed_kmh": 3,
  "wind_angle_deg": 345,
  "gust_speed_kmh": 9,
  "gust_angle_deg": 23,
  "rain_mm": 0,
  "sum_rain_1h_mm": 0,
  "sum_rain_24h_mm": 0.1,
  "today_outdoor_min_c": 6.1,
  "today_outdoor_max_c": 9.8,
  "today_outdoor_min_at": "2026-04-26T08:46:41Z",   // omitted when not reported
  "today_outdoor_max_at": "2026-04-26T14:46:41Z",
  "modules": [
    { "type": "NAModule1", "name": "Outdoor", "battery_percent": 83, "reachable": true, "rf_status": 80, "last_seen": "2026-04-26T16:33:15Z" },
    { "type": "NAModule2", "name": "Smart Anemometer", "battery_percent": 52, "reachable": true, "rf_status": 73, "last_seen": "2026-04-26T16:33:21Z" }
  ]
}
```

> `stale` is `true` when either `fetched_at` or `outdoor_timestamp` is older than the staleness window (see `/forecast`): the server keeps answering with its last good reading while fetches from Netatmo are failing, and a module that has gone quiet leaves its last values on the dashboard while the base keeps reporting. A client must check this flag (the bundled app shows a warning) rather than treat every `200` as live. `timestamp` alone can't show the second case, because it belongs to the base station.
>
> `outdoor_available` is `false` when the outdoor module is missing or unreachable; `outdoor_*`, `apparent_temp_c` and the `today_outdoor_*` values are then zeros rather than readings. The service also refuses to record such a reading in its history, so a dead battery can't plant fake 0 °C points in the forecast.
>
> Before the first reading has been fetched, `/current` answers `503`.

### `GET /health`

Liveness check.

```json
{ "status": "ok", "time": "2026-04-26T16:28:38Z" }
```

### `GET /debug`

Inspection endpoint: shows the number of observations in the in-memory store and recent temperature samples. Useful for diagnosing data-loading issues.

### Errors

Every response, including errors, is JSON. Errors look like `{ "error": "message" }`: `404` for an unknown path, `405` (with an `Allow` header) for anything but `GET`/`HEAD`, and `503` when `/current` has no reading yet. Messages are deliberately generic; details go to the server log.

---

## Configuration reference

All settings are read from `.env` (loaded automatically) or from real environment variables (which take precedence). A setting that is present but invalid (not a number, out of range, …) stops the service at startup with a message naming it; it is never silently replaced by the default.

| Variable | Default | Description |
|---|---|---|
| `NETATMO_CLIENT_ID` | _(required)_ | Netatmo OAuth2 client ID |
| `NETATMO_CLIENT_SECRET` | _(required)_ | Netatmo OAuth2 client secret |
| `NETATMO_REDIRECT_URL` | `http://localhost:8080/auth/callback` | Must match Netatmo app settings. During first-run authorisation the service listens on this URL's host and port (not `PORT`). |
| `NETATMO_STATION_ID` | _(auto)_ | Main station MAC address |
| `NETATMO_OUTDOOR_MODULE_ID` | _(auto)_ | Outdoor module MAC (NAModule1) |
| `NETATMO_WIND_MODULE_ID` | _(auto)_ | Wind gauge MAC (NAModule2) |
| `NETATMO_RAIN_MODULE_ID` | _(auto)_ | Rain gauge MAC (NAModule3) |
| `TOKEN_FILE` | `~/.mycast/tokens.json` | OAuth2 token persistence path (a leading `~/` is expanded) |
| `BIND_ADDR` | `127.0.0.1` | Address the API listens on. The API is unauthenticated, so it is loopback-only by default; use `0.0.0.0` to reach it from other devices on a trusted network. |
| `PORT` | `8080` | HTTP server port (1–65535) |
| `FETCH_INTERVAL_MIN` | `30` | How often to poll the station (minutes, 1–1440). The staleness window is twice this, with a 30-minute floor. |
| `HISTORY_DAYS` | `7` | Days of hourly history to load and retain (2–30). Also the span of Open-Meteo hindcast used to learn the bias correction. |
| `OPENMETEO_ENABLED` | `true` | Use ECMWF (via Open-Meteo) with local bias correction as the primary forecast; falls back to the station-only model automatically if unreachable. No API key needed. |

---

## Project layout

```
server/
├── main.go                    # local mode: startup → scheduler loop → HTTP server
├── ingest/ingest.go           # the pipeline both modes share: tick, catch-up, engine construction
├── config/config.go           # .env loader + typed config struct
├── netatmo/
│   ├── auth.go                # OAuth2 flow, TokenStore interface (file or DynamoDB), non-interactive refresh
│   ├── client.go              # getstationsdata + getmeasure API client (stateless)
│   └── models.go              # Netatmo JSON types + Observation / Current / Station structs
├── openmeteo/client.go        # Open-Meteo (ECMWF) forecast client
├── noaa/client.go             # NOAA SWPC Kp index forecast client (aurora)
├── store/
│   ├── timeseries.go          # thread-safe in-memory buffer: one observation per clock hour, merged by field group
│   └── current.go             # cache of the latest live reading served by /current
├── forecast/
│   ├── holtwinters.go         # deterministic damped-trend Holt-Winters (station-only fallback)
│   ├── mos.go                 # local bias correction (Model Output Statistics) for the ECMWF path
│   ├── aurora.go              # geomagnetic latitude + Kp-based aurora visibility heuristic
│   ├── engine.go              # ECMWF+MOS path with station-only fallback, local-day bucketing, caching
│   ├── grid.go                # fills gaps so the station-only model sees one sample per hour
│   ├── condition.go           # WMO weather code → short description
│   ├── apparent.go            # "feels like" temperature
│   ├── precipitation.go       # persistence + climatological precipitation model
│   ├── wind.go                # circular mean for wind direction aggregation
│   └── models.go              # Forecast / DayForecast JSON response types
├── api/
│   ├── server.go              # routing, JSON 404/405, bearer token, listener + graceful shutdown
│   ├── handlers.go            # /forecast  /current  /health  /debug
│   └── current.go             # /current wire format (decoupled from the Netatmo types)
├── dynamo/                    # DynamoDB persistence + the API's stored-data reader (AWS mode)
├── serverless/                # one stateless ingest run, secret loading (AWS mode)
├── lambdahttp/                # runs the API handler behind a Lambda Function URL (AWS mode)
└── cmd/
    ├── ingest-lambda/         # scheduled function: fetch, update, recompute, save
    ├── api-lambda/            # Function URL function: serve from DynamoDB
    └── mycast-auth/           # one-off OAuth authorisation, stores the token

deploy/                        # AWS CDK app + build script (see deploy/README.md)
```

`app/` is a SwiftUI client (Xcode project) that consumes this API — see `app/MyCast/Networking/WeatherService.swift` for the request layer. The server address is the `MyCastAPIBaseURL` key in `app/MyCast/Info.plist` (default `http://localhost:8080`).

## Tests

```bash
cd server && go test -race ./...
cd deploy/cdk && npm test          # the CDK stack's assertions
```

The golden files in `app/MyCastTests/Fixtures/` are the server's real JSON for each response shape (full forecast, forecast without aurora, station-only fallback, `/current` with and without the outdoor module). The Go tests fail if the server's output drifts from them, and the Swift tests (`WeatherDecodingTests`) decode the same files, so a change that the app can't read is caught on one side or the other. After an intentional API change:

```bash
cd server && go test ./forecast ./api -run Golden -update
```

then run the app's tests (`xcodebuild test -project app/MyCast.xcodeproj -scheme MyCast -destination 'platform=iOS Simulator,name=iPhone 17' -only-testing:MyCastTests`).

---

## Dependencies

| Package | Purpose |
|---|---|
| `golang.org/x/oauth2` | Netatmo OAuth2 authentication |
| `aws-sdk-go-v2` (DynamoDB, SSM, config), `aws-lambda-go` | The AWS mode only: persistence, secrets, the Lambda runtime. Imported by `dynamo`, `serverless`, `lambdahttp` and `cmd/` — local mode links none of it |
| Standard library only | All other functionality |

External services: [Netatmo API](https://dev.netatmo.com/) (station data, requires an account), [Open-Meteo](https://open-meteo.com/) (ECMWF forecast, free, no key — set `OPENMETEO_ENABLED=false` to disable and run station-only), and [NOAA SWPC](https://www.swpc.noaa.gov/) (Kp index forecast for aurora, free, no key — automatically enabled alongside Open-Meteo).
