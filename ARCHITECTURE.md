# Architecture

mycast is a Go service that reads a Netatmo weather station, blends an ECMWF forecast with a bias correction learned from the station's own readings, and serves the result as JSON to a SwiftUI app.

This page shows how the pieces fit and how data moves. For the forecasting maths see [FORECAST.md](FORECAST.md); for setup and the API reference see [README.md](README.md).

## System overview

```mermaid
flowchart LR
    subgraph ext["External services"]
        NA["Netatmo API<br/>station data + OAuth2"]
        OM["Open-Meteo<br/>ECMWF forecast"]
        NO["NOAA SWPC<br/>Kp index forecast"]
    end

    subgraph srv["server/ (Go)"]
        direction TB
        MAIN["main<br/>startup + scheduler"]
        NET["netatmo<br/>auth + client"]
        TS[("store.TimeSeries<br/>one slot per clock hour")]
        CC[("store.CurrentCache<br/>latest live reading")]
        ENG["forecast.Engine<br/>ECMWF + MOS, or Holt-Winters"]
        API["api<br/>HTTP, loopback by default"]
    end

    APP["app/ (SwiftUI)<br/>iOS client"]

    NA -->|"getstationsdata<br/>getmeasure"| NET
    NET --> MAIN
    MAIN -->|"live readings: Append"| TS
    MAIN -->|"hourly history: Backfill"| TS
    MAIN -->|"latest reading: Put"| CC
    MAIN -->|"Compute after each tick"| ENG
    TS --> ENG
    OM -->|"hourly + sun times"| ENG
    NO -->|"Kp, optional"| ENG
    ENG -->|"Fresh"| API
    CC -->|"Latest"| API
    TS -->|"UpdatedAt, /debug"| API
    API -->|"/forecast /current<br/>/health /debug"| APP
```

Two rules shape the layout:

- **Requests never call Netatmo.** The scheduler is the only thing that talks to it; `/current` and `/forecast` are answered from memory.
- **The Netatmo client is stateless.** What it learns about the station comes back as a `netatmo.Station` value on each reading, and history calls take one as an argument. Caching lives in `store`, owned by `main`.

## Packages

| Package | Responsibility | Depends on |
|---|---|---|
| `main` | Startup order, the scheduler tick, wiring | everything below |
| `config` | `.env` + environment loading, validation of ranges | none |
| `netatmo` | OAuth2 flow and token file; `GetCurrent`, `GetHistory`; the `Observation`, `Current`, `Station` types | `oauth2` |
| `store` | `TimeSeries` (hourly buffer, merge by field group) and `CurrentCache` | `netatmo` |
| `openmeteo` | ECMWF hourly forecast and sunrise/sunset | none |
| `noaa` | Kp index forecast for the aurora estimate | none |
| `forecast` | `Engine`: forecast paths, local-day bucketing, staleness | `store`, `netatmo`, `openmeteo`, `noaa` |
| `api` | Routes, JSON wire types, `stale` flags, graceful shutdown | `forecast`, `store`, `netatmo` |

## Startup

`main` runs these steps in order. A failure at any step except history exits the process.

```mermaid
flowchart TD
    A["config.Load<br/>validate ranges, .env"] --> B{"client ID and<br/>secret set?"}
    B -- no --> X1(["exit: configuration error"])
    B -- yes --> C["Authenticator.GetHTTPClient"]
    C --> C1{"stored token<br/>usable?"}
    C1 -- "valid" --> D
    C1 -- "expired" --> C2["refresh and persist"]
    C1 -- "none or refresh failed" --> C3["interactive OAuth:<br/>print URL, wait for callback"]
    C2 --> D
    C3 --> D
    D["Client.GetCurrent<br/>discover Station, location, timezone"] --> D1{"reachable?"}
    D1 -- no --> X2(["exit: could not reach station"])
    D1 -- yes --> E["CurrentCache.Put"]
    E --> F["Client.GetHistory<br/>last HISTORY_DAYS"]
    F --> F1{"ok?"}
    F1 -- yes --> G["TimeSeries.Backfill<br/>history never marks the station as reporting"]
    F1 -- "no: warn and continue" --> H
    G --> H["TimeSeries.Append live reading<br/>if the outdoor module is available"]
    H --> I["Build forecast.Config<br/>StaleAfter, station timezone,<br/>Open-Meteo and NOAA if location known"]
    I --> J["Engine.Compute<br/>initial forecast"]
    J --> K["start scheduler goroutine"]
    K --> L["api.Server.Listen"]
    L --> L1{"port free?"}
    L1 -- no --> X3(["exit: cannot listen"])
    L1 -- yes --> M(["serve until SIGINT or SIGTERM"])
```

## Scheduler tick

Every `FETCH_INTERVAL_MIN` the scheduler runs `fetchAndUpdate`. It is the only writer of live data.

```mermaid
flowchart TD
    T(["tick"]) --> A["Client.GetCurrent"]
    A --> A1{"error?"}
    A1 -- yes --> E1(["log, change nothing"])
    A1 -- no --> B["CurrentCache.Put<br/>/current serves this even if outdoor is down"]
    B --> C{"Current.Observation ok?<br/>outdoor module reachable and dated"}
    C -- no --> E2(["log, skip: no fake 0 °C is stored"])
    C -- yes --> D["TimeSeries.Append<br/>stamped with the outdoor module's own time"]
    D --> F["Client.GetHistory<br/>last 3 hours"]
    F --> F1{"error?"}
    F1 -- yes --> H
    F1 -- no --> G["keep completed hours only<br/>drop the hour in progress"]
    G --> G2["TimeSeries.Backfill<br/>merge by field group, adds rain"]
    G2 --> H["Engine.Compute"]
    H --> Z(["done"])
```

### How data is written to the store

Observations say which field groups they really carry (`Observation.Has`). A write only overwrites the groups it provides, so an hour with no wind aggregate keeps its stored wind.

```mermaid
flowchart LR
    L["Live reading<br/>outdoor + wind, no rain"] --> AP["Append"]
    H["Hourly history<br/>outdoor, wind, rain as reported"] --> BF["Backfill"]

    AP --> M["merge into the hour's slot<br/>only the field groups provided"]
    BF --> M
    M --> S[("one slot per clock hour")]

    AP --> U{"measurement time newer<br/>than any seen?"}
    U -- yes --> UP["UpdatedAt = now"]
    U -- "no: repeated or older" --> NO["UpdatedAt unchanged"]
    BF -. "never touches" .-> UP
```

| | `Append` | `Backfill` |
|---|---|---|
| Source | live reading each tick | Netatmo hourly aggregates |
| Rain | never (a rolling hour sum would double-count) | yes, the authoritative clock-hour total |
| Order | ignores hours older than the newest | inserts anywhere |
| Counts as "station reporting" | only for a newer measurement | never |

## Forecast computation

`Engine.Compute` always produces a forecast. The ECMWF path is preferred; any failure falls back to the station-only model and is logged.

```mermaid
flowchart TD
    S(["Engine.Compute<br/>serialised by computeMu"]) --> A{"any observations?"}
    A -- no --> N["empty forecast<br/>days = [] , stale"]
    A -- yes --> B{"Open-Meteo<br/>configured?"}
    B -- no --> ST
    B -- yes --> C["openmeteo.Fetch<br/>past days + 4 forecast days"]
    C --> D{"fetch ok and arrays<br/>consistent and complete<br/>for the 72 h window?"}
    D -- no --> LOG["log reason"] --> ST
    D -- yes --> E["fit hourly bias<br/>actual minus predicted, skipping NaN"]
    E --> F["apply bias to temperature,<br/>humidity, wind speed"]
    F --> G["NOAA Kp<br/>optional"]
    G --> G1{"Kp available?"}
    G1 -- yes --> G2["aurora estimate<br/>geomagnetic latitude, cloud, darkness"]
    G1 -- no --> G3["aurora omitted"]
    G2 --> BD
    G3 --> BD
    BD["buildDays<br/>local calendar days, max 3"] --> OUT(["cache + return<br/>model ecmwf_ifs025+local-bias-correction"])

    ST["computeFromStation<br/>hourlyGrid fills gaps"] --> H["damped Holt-Winters<br/>temp, humidity, speed, wind sin and cos"]
    H --> I["persistence + climatology<br/>precipitation"]
    I --> BD2["buildDays<br/>no sun, aurora or condition"]
    BD2 --> OUT2(["cache + return<br/>model station-holtwinters"])
```

`buildDays` groups hours by the **station's local calendar day**, so the first day holds only the hours remaining today. Darkness for each hour is decided from the most recent sunrise or sunset before it.

## Serving requests

Every response is JSON, including `404`/`405`. Only `GET` and `HEAD` are accepted.

```mermaid
flowchart TD
    R(["HTTP request"]) --> P{"path"}
    P -- "/health" --> H1["status ok"]
    P -- "/debug" --> H2["count, first and last timestamp,<br/>last 5 temperatures"]
    P -- "/forecast" --> F0["Engine.Fresh"]
    P -- "/current" --> C0["CurrentCache.Latest"]
    P -- other --> E404(["404 JSON"])

    F0 --> F1{"cache missing or<br/>older than StaleAfter?"}
    F1 -- yes --> F2["recompute once<br/>concurrent callers share it"]
    F1 -- no --> F3
    F2 --> F3["stale = no days<br/>or no new measurement<br/>for StaleAfter"]
    F3 --> F4(["200 forecast"])

    C0 --> C1{"a reading<br/>exists?"}
    C1 -- no --> C2(["503 JSON"])
    C1 -- yes --> C3["stale = fetched_at too old<br/>or outdoor measurement too old"]
    C3 --> C4(["200 current"])
```

`StaleAfter` is `2 × FETCH_INTERVAL_MIN`, never below 30 minutes, because Netatmo modules report roughly every ten minutes. The two `stale` flags are defined differently on purpose:

| Endpoint | `stale` is true when |
|---|---|
| `/forecast` | there is no data, or the store has seen no *newer measurement* for `StaleAfter` |
| `/current` | `fetched_at` is older than `StaleAfter`, **or** `outdoor_timestamp` is |

Both still serve the last good data; clients are expected to show a warning rather than hide it.

## Invariants worth knowing

- **One slot per clock hour.** Timestamps are floored to the hour, so a week of capacity really is a week of wall-clock time.
- **Readings are dated by the module that measured them.** The base station's clock is not used for outdoor values.
- **Zero means "no data" only where the type says so.** Use `Observation.Has` to tell a measured calm from a missing wind value.
- **Loopback by default.** The API has no authentication, so `BIND_ADDR` defaults to `127.0.0.1`.
- **Fixtures are shared with the app.** `app/MyCastTests/Fixtures/*.json` are generated by the Go golden tests (`go test ./forecast ./api -run Golden -update`) and decoded by the Swift tests, so a response change one side can't read fails on one of them.
