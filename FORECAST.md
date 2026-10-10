# Forecasting Model — Technical Reference

This document describes the forecasting algorithms used by mycast, why they were chosen, and how to reason about their outputs. It is intended as a reference for extending, tuning, or replacing the models.

All file paths below (`main.go`, `forecast/`, `netatmo/`, etc.) are relative to [`server/`](server/), the Go backend's root.

---

## Overview

The service produces a 3-day hourly forecast, refreshed every time new station data arrives (every 30 minutes by default). There are two forecast paths:

| Path | When used | Variables |
|---|---|---|
| **ECMWF + local bias correction (primary)** | Open-Meteo is reachable and the station's location is known | Temperature, humidity, wind speed, wind direction, precipitation |
| **Station-only (fallback)** | Open-Meteo is unavailable, disabled, or the station's location hasn't been discovered yet | Temperature, humidity, wind speed, wind direction, precipitation |

Precipitation uses ECMWF's own amount + probability on the primary path, and the local persistence + climatology model on the fallback path (see below) — unlike temperature/humidity/wind speed, it does **not** get a local bias correction on the primary path.

Every `/forecast` response includes a `model` field naming which path produced it (e.g. `ecmwf_ifs025+local-bias-correction` or `station-holtwinters (open-meteo unavailable)`), so consumers always know the forecast's provenance.

---

## Why blend an NWP model at all

A single weather station's own history contains no information about approaching fronts, pressure systems, or anything else driven by large-scale atmospheric dynamics — that information simply isn't present in one location's past readings, no matter how much of it you accumulate. A purely local statistical model (see "Station-only fallback" below) can therefore never reliably forecast a genuine weather change 1–3 days out; at best it learns the local diurnal/seasonal cycle and extrapolates it.

ECMWF's IFS model, by contrast, assimilates a global grid of satellite, radiosonde, and surface observations and solves the actual physics — it has access to exactly the information a single station lacks. Backtesting against this station's real history (7 days, matched at a genuine 72h lead time) showed:

| Model | MAE (°C) | RMSE (°C) |
|---|---|---|
| Seasonal-naive (persistence, 72h ago) | 1.52 | 1.91 |
| Station-only damped Holt-Winters | 1.12 | 1.53 |
| **ECMWF, raw (Open-Meteo, exact station coordinates)** | **0.74** | **0.93** |

> **About these numbers.** They are one-off measurements from a single calm 7-day window on one station. The harness and station data behind them are not in this repository, so they cannot be reproduced from it, and the same applies to the damping comparison under "Why damped, not plain, trend" and the precipitation tables below. Read them as the evidence that motivated the design, not as a maintained benchmark. (The unit tests do pin the *behaviour* that came out of them: `TestHoltWintersDampsAStrongTrend` fails if damping is removed.)

ECMWF meaningfully outperforms the pure local model at this lead time, as expected. The remaining error is largely the gap between ECMWF's ~28km grid point and the station's exact microclimate — which is what the local bias correction below is for.

---

## ECMWF + Local Bias Correction (Model Output Statistics)

This is a standard technique used by real local-forecast providers: take a physics-based NWP forecast and apply a statistical correction learned from a specific location's own observations, rather than trying to reproduce NWP skill from local data alone.

**Source:** [Open-Meteo](https://open-meteo.com/) (`openmeteo/client.go`), free, no API key, requested at the station's exact coordinates (auto-discovered from the Netatmo API's `place.location` field — see `netatmo/client.go`). Model requested: `ecmwf_ifs025`.

**Bias fitting (`forecast/mos.go`):**
```
For each hour-of-day bucket h in [0, 23]:
    bias[h] = mean( station_actual[t] - ecmwf_predicted[t] )
              over all recent timestamps t where t % 24h == h
```
Open-Meteo's `past_days` parameter supplies its own recent analysis-quality hourly values, which are compared against the station's actual observations for the same hours. A bucket needs at least 2 matched samples before its bias is trusted (`minBiasSamples`); otherwise it's left at zero rather than extrapolating from too little evidence.

**Applying the correction:**
```
corrected_forecast[h] = ecmwf_forecast[h] + bias[hour_of_day(h)]
```

**Scope of the correction:** temperature, humidity, and wind speed are bias-corrected this way. Wind direction is **not** corrected — it's a circular quantity (0°/360° are adjacent, not opposite), and a linear additive bias would be meaningless across the wrap. Raw ECMWF wind direction is used as-is. Precipitation is sourced from ECMWF (amount + probability) but is also **not** bias-corrected — see below.

**Fallback behavior:** if the Open-Meteo request fails (network error, timeout, bad response) or doesn't cover the full 72-hour forecast window, the engine logs the failure and falls back to the station-only model for that cycle — every 30-minute retry gets a fresh chance to reach Open-Meteo again.

---

## Station-only Fallback — Damped-Trend Holt-Winters

Used when Open-Meteo is unavailable, disabled (`OPENMETEO_ENABLED=false`), or the station's location isn't known yet.

Deterministic, pure Go, additive triple exponential smoothing (Holt-Winters / Gardner's method) with a **damped trend** — smoothing parameters (α, β, γ) and the damping factor (φ) are chosen by grid search minimizing one-step-ahead SSE, not by gradient descent or any random initialization. Given the same input series, it always produces the same output.

```
level[t]    = α·(y[t] - seasonal[t%24]) + (1-α)·(level[t-1] + φ·trend[t-1])
trend[t]    = β·(level[t] - level[t-1]) + (1-β)·φ·trend[t-1]
seasonal[t%24] = γ·(y[t] - level[t]) + (1-γ)·seasonal[t%24]

forecast[h] = level[n] + (Σ φ^i for i=1..h)·trend[n] + seasonal[(n+h-1)%24]
```

**Why damped, not plain, trend:** plain (undamped) Holt-Winters extrapolates a linear trend indefinitely — a short-term decline compounds into an implausible value days out. This was measured directly: on a real backtest, undamped Holt-Winters produced MAE 5.66°C (temperature drifted from 14°C down to 4°C while actual temperatures recovered to 16°C), while the damped variant produced MAE 1.19°C on the same data. The damping factor (φ < 1) causes the trend's influence to decay toward flat over the forecast horizon instead of continuing indefinitely.

**Why not a neural net (history):** an earlier version of this model used a small MLP (28 inputs, 32→16 hidden nodes) trained from scratch on every 30-minute cycle, predicting *anomalies* (deviations from a trailing 24h mean) to avoid unbounded drift. It produced physically implausible forecasts intermittently (e.g. 27°C in September) because each retrain used an unseeded random weight initialization and shuffle order on only ~140 training examples — a stress test showed the same input data producing forecasts ranging from 1.8°C to 25.4°C across 15 retrains. Holt-Winters has no random initialization at all, so this failure mode is structurally impossible.

---

## Wind Direction — Circular Decomposition (station-only path)

Wind direction is a circular variable: 1° and 359° are 2° apart, not 358° apart. Standard regression/smoothing fails on circular targets directly.

**Solution (station-only path only):** decompose the angle into its unit-vector components, forecast each independently with Holt-Winters, then reconstruct.

```
Training:
    sin_θ[t] = sin(θ[t] · π/180)
    cos_θ[t] = cos(θ[t] · π/180)
    → run Holt-Winters on sin_θ and on cos_θ independently

Forecast:
    sin_pred[h], cos_pred[h] = Holt-Winters forecasts
    θ_pred[h] = atan2(sin_pred[h], cos_pred[h]) · 180/π
    if θ_pred[h] < 0: θ_pred[h] += 360
```

**Circular mean** (used for daily aggregation in both paths):
```
θ_mean = atan2(mean(sin θᵢ), mean(cos θᵢ)) · 180/π
```

The ECMWF path skips this entirely and uses ECMWF's own wind direction forecast directly.

---

## Precipitation Model

**Primary path (ECMWF):** amount from Open-Meteo's `precipitation` field, probability from its `precipitation_probability` field (0–100%, converted to 0–1) — both taken directly from the response, with no local bias correction. Precipitation's zero-inflated, skewed distribution makes an additive MOS-style correction (like the one used for temperature/humidity/wind speed) risky to apply with only a week or two of local rain history to calibrate against; this is worth revisiting once more history accumulates.

**Why not bias-correct or blend with the local signal (evidence):** the local persistence+climatology model (below) was backtested against this station's real rain history at a genuine 72h lead:

| Model | Hit rate (rain caught) | False alarm rate | Raw accuracy | Brier score |
|---|---|---|---|---|
| Local persistence+climatology | 0% (0 of 8 real rain hours) | 0% | 89%* | 0.107 |
| Climatological baseline (constant ~2% chance) | — | — | — | 0.107 (identical) |
| ECMWF, genuine 72h-ahead (amount, threshold 0.1mm) | 38% (3 of 8) | 28% | 68% | not backtestable† |

\* That 89% is misleading: rain only occurred in ~11% of hours, so "always predict no rain" scores 89% while catching zero real rain events. The local model's Brier score being statistically identical to a constant climatological guess means it had **no real forecasting skill** in this test — never once flagged an actual rain event.

† Open-Meteo doesn't archive `precipitation_probability` at fixed historical lead times, so unlike the raw amount forecast (validated above), that specific derived probability field's historical accuracy hasn't been backtested — it's used because it's a materially better signal than a hand-rolled heuristic, not because its skill has been directly measured here.

Given the local model showed no measurable skill above naive climatology, blending it into the ECMWF signal wouldn't add anything at this point — unlike temperature, where the local model actually won the fallback backtest for a calm week.

**Fallback path (station-only): persistence + climatological pattern**

```
For each forecast horizon h:
    1. Climatological probability:
           prob_clim[h] = fraction of hours with rain in historical window
                          (bucketed by hour-of-day)

    2. Persistence signal:
           recent_rate = exponential_smooth(last rain observations, α=0.4)
           prob_pers   = clamp(recent_rate / max(recent_rate, 1), 0, 1)

    3. Blend:
           blended_prob = 0.5 · prob_pers + 0.5 · prob_clim[h%24]
           blended_amt  = 0.5 · recent_rate + 0.5 · hourly_mean_amount[h%24]

    4. Confidence damping (skill decays with horizon):
           damping = exp(−0.02 · h)
           prob[h] = damping · blended_prob + (1 − damping) · 0.30
           amt[h]  = damping · blended_amt
```

The base rate of 0.30 (30%) is a conservative climatological prior for northern European climates. Adjust via the source constant in `precipitation.go` if needed. This path exists purely so precipitation is never left without a forecast if Open-Meteo is unavailable — per the backtest above, treat its probability output as low-confidence.

---

## Condition Summary and Apparent Temperature

Two derived fields added for API consumers (e.g. a mobile app's home screen):

- **Condition** (`forecast/condition.go`): ECMWF's `weather_code` (WMO table 4677) is mapped to a short human-readable summary (e.g. `51` → "Light drizzle"). Only available on the ECMWF path — the station-only fallback has no source for a categorical condition and reports `"Unknown"`. The day-level summary uses the code at the hour nearest local midday as representative (for a day already past noon, its first remaining hour).
- **Apparent temperature** (`forecast/apparent.go`): computed locally in Go from temperature + humidity + wind speed (the Australian Bureau of Meteorology formula — the same one Open-Meteo itself documents using), rather than taken from Open-Meteo's `apparent_temperature` field. This makes it available identically on both the ECMWF and station-only paths, from whatever temp/humidity/wind forecast each path already produces, rather than being another field that silently disappears on fallback.
- **Sunrise/sunset**: sourced from Open-Meteo's `daily` block (astronomical, not a weather-model output — so it's identical across NWP models, but still only fetched on the ECMWF path). Unlike apparent temperature, this one *is* only available when Open-Meteo is reachable — a local sunrise/sunset calculation from the station's own lat/lon would be straightforward to add later if the station-only fallback needs it too, but hasn't been implemented.

---

## Aurora Visibility (`forecast/aurora.go`)

Open-Meteo/ECMWF has no concept of aurora — it's driven by geomagnetic activity (solar wind, Earth's magnetic field), not atmospheric physics, so it comes from a separate source entirely: **NOAA's Space Weather Prediction Center** (`noaa/client.go`), specifically their [3-hour-resolution Kp index forecast](https://services.swpc.noaa.gov/products/noaa-planetary-k-index-forecast.json) (free, no key, ~3 days of predicted/estimated values plus recent observed history).

**Why Kp alone isn't enough to answer "can I see it from here":** the Kp index is global, but aurora visibility is local — it depends on how close a location is to the auroral oval, which is centered on the *geomagnetic* pole, not the geographic one. Finland sees aurora at latitudes where a naive geographic model would say it shouldn't, because the geomagnetic pole is offset toward northern Canada. The pipeline:

1. **Geomagnetic latitude** (`geomagneticLatitude`): the standard dipole approximation, using the current geomagnetic pole's coordinates (~80.7°N, 72.7°W). Verified against Fairbanks, Alaska — a well-documented public aurora reference point: the formula gives ~65.6°N geomagnetic latitude, matching Fairbanks' known reputation for near-nightly aurora visibility at low Kp (a stronger, more specific check than just "is this formula implemented correctly").
2. **Required Kp** (`requiredKp`): inverts the standard published table mapping Kp → the auroral oval's equatorward boundary (geomagnetic latitude), via linear interpolation, to answer "what Kp does *this* location need."
3. **Combined probability** (`auroraProbabilityPct`): a deliberately simple heuristic — zero in daylight (using the same sunrise/sunset data as above), zero below the required Kp, scaling linearly from 0% at the threshold to 100% three Kp steps above it, then derated by real cloud cover (also from the same Open-Meteo response already fetched for the weather forecast). This is explicitly **not** a validated aurora nowcast model — it doesn't account for solar wind speed or IMF Bz orientation, the inputs that actually drive short-term aurora prediction (e.g. NOAA's OVATION model, which has a ~30-70 minute lead time and wasn't integrated here — see Known Limitations). It answers "is it geomagnetically active enough, with clear dark sky," which is the right question for a multi-day forecast.

Aurora data is only computed on the ECMWF path (it needs cloud cover and sunrise/sunset, neither of which the station-only fallback has) and is a bonus on top of it — if NOAA is unreachable, the rest of the forecast is unaffected and aurora fields are simply omitted for that cycle.

---

## Staleness Self-Healing

The `Forecast` struct is fully cached at `Compute()` time, including its day labels (`buildDays` derives "today"/"tomorrow" from the clock when it runs, not when the response is served). If the background scheduler stops ticking for any reason — repeated upstream failures, or the whole process being paused (observed directly during development: this dev environment paused between sessions, and the cached forecast kept serving the wrong day for 12+ hours afterward with zero scheduler log lines) — the cached forecast's date labels go silently wrong indefinitely, with no indication beyond `generated_at`.

`Engine.Fresh()` (used by the `/forecast` handler instead of the raw `Latest()` accessor) fixes this: if the cached forecast is older than `staleAfter` (set in `main.go` to `2 × FETCH_INTERVAL_MIN`, but never below 30 minutes), it forces a synchronous `Compute()` before responding. Concurrent requests on a stale cache share one recompute (a mutex plus a re-check), and the scheduler's own `Compute()` takes the same lock, so a slow computation can never overwrite a newer result.

A recompute stamps the forecast with the current time, so the forecast's own age can't say whether the *data* is current. The `stale` field therefore reports station silence instead: it is `true` when the time series has accepted no *live reading* for longer than `staleAfter` (`store.TimeSeries.UpdatedAt`), or when there is no data at all (in which case `days` is `[]`). Only `Append` (a live reading) moves `UpdatedAt`, and only when its *measurement time* is newer than any seen before; `Backfill` (history) never does. So a start-up while the outdoor module is offline, where only days-old history loads, is correctly `stale` instead of looking fresh for two intervals, and so is a module that has gone quiet but is still flagged reachable: the base station keeps ticking and the service keeps re-reading the module's last values, but a repeated measurement is not a new one. The 30-minute floor exists because staleness now follows the module's own clock and Netatmo modules report only every ~10 minutes; a short `FETCH_INTERVAL_MIN` must not make a healthy station look stale. Note the recompute works from whatever is already in the time series and pulls a fresh ECMWF forecast; it does not itself re-fetch Netatmo — a stalled scheduler still gets correctly labelled days, but from older station history, and is flagged `stale` so clients can say so.

**In the AWS deployment the API does not recompute at all.** `/forecast` returns the forecast the last ingest run stored, and `stale` is computed from the stored staleness state (`META`) by the same rule. The day labels of a stored forecast are fixed at the time it was computed, so a forecast that is old because ingest stopped is served flagged `stale` rather than rebuilt from data that is just as old. The state that makes this work — the newest measurement time and when it was first seen — is persisted with the observations and restored by `store.TimeSeries.Restore`. A reading from an unreachable outdoor module is deliberately not stored (it would be a fake 0 °C), so an offline module also turns `stale` on after the staleness window.

**Which clock stamps a reading.** A live observation is stamped with the *outdoor module's* measurement time (`dashboard_data.time_utc`, falling back to the module's `last_seen`; a reading with neither is unusable), not the indoor base station's. A quiet module's old values therefore land in the hour they were measured, not in the current one.

---

## Days, Hours and Time Zones

The model works on a 72-hour horizon starting at the next whole hour. What the API calls a *day* is a **calendar day in the station's timezone** (Netatmo reports it with the station's location; if it's missing or unrecognised the service falls back to UTC and says so in the log). The first day therefore holds only the hours that remain today, and at most three days are returned; hours that would spill onto a fourth are dropped.

The store keeps one observation per clock hour (timestamps are floored to the hour; a later reading in the same hour replaces the earlier one), so its capacity really does span `HISTORY_DAYS` of wall-clock time.

**Rain is reconciled from history.** A live reading cannot supply a clock-hour rain total: the station only reports a rolling last-hour sum, which at 10:05 covers 09:05-10:05 and would overlap the completed 09:00 bucket, counting that rain twice. Live observations therefore carry no rain. On every scheduler tick the service re-reads the last three hours of Netatmo's hourly history and merges it into the *completed* hours (rain included); the hour in progress keeps its live reading until it ends. Merging is by field group (outdoor, wind, rain): an observation says which groups it really carries (`Observation.Has`), and only those overwrite what is stored. An hour for which Netatmo has no wind aggregate therefore keeps the wind already recorded for it rather than being reset to a calm, and a genuinely measured 0 km/h still counts as data. Rain for an hour is thus provisional (zero) for up to one fetch interval after the hour closes, and a failed history refresh only delays the correction to a later tick. The station-only model needs a series with no missing hours, so before fitting it the engine fills gaps (`forecast/grid.go`: interpolation for temperature, humidity and speeds; last value for directions; zero for rain) and models through any gap between the newest observation and the first reported hour, keeping every value on the hour it is labelled with. Precipitation climatology is bucketed by the hour-of-day of each sample's timestamp, not by its position in the slice.

Sunrise and sunset come from Open-Meteo as instants; the date a day reports is the local date each instant falls on, and darkness at any hour is decided from the nearest preceding sun event.

---

## Data Flow

```
Netatmo API                          Open-Meteo API (ECMWF)
    │ (every 30 min)                     │ (every 30 min, if enabled)
    ▼                                    ▼
store.TimeSeries               openmeteo.Client.Fetch()
(circular buffer,               (past_days + forecast_days,
 168 hourly observations)        exact station coordinates)
    │                                    │
    └──────────────┬─────────────────────┘
                    ▼
         engine.Compute()
                    │
       ┌────────────┴────────────┐
       │ Open-Meteo reachable?   │
       └────────────┬────────────┘
            yes ▼         ▼ no / error
  computeFromECMWF()   computeFromStation()
  - fit hourly bias     - Holt-Winters per variable
    (station vs ECMWF     (temp, humidity, speed,
     recent hindcast)      sin/cos wind angle)
  - apply bias to        - precipForecast()
    ECMWF forecast
  - raw ECMWF wind dir
  - precip: raw ECMWF
    amount + probability
       └────────────┬────────────┘
                    ▼
              buildDays()
                    │
                    ▼
         Forecast{Model: "...", Days: [...]}
         cached in engine, served by GET /forecast
```

---

## Tuning Guide

| Parameter | Location | Effect |
|---|---|---|
| `OPENMETEO_ENABLED` | `.env` | Disable to force the station-only model even when Open-Meteo is reachable. |
| `omPastDays` | `engine.go` | How much recent history to request from Open-Meteo for bias calibration. More days → more samples per hour-of-day bucket, at the cost of a larger request. |
| `omForecastDaysBuffer` | `engine.go` | Extra forecast days requested beyond 3, to guarantee the response always covers a full 72h from "now" regardless of time of day. |
| `minBiasSamples` | `mos.go` | Minimum matched samples before an hour-of-day bias bucket is trusted; below this, that hour is left uncorrected. |
| `hwSeason` | `holtwinters.go` | Seasonal period in samples — 24 for hourly data (daily cycle). |
| `HISTORY_DAYS` | `.env` | More history → more Holt-Winters seasonal-fit accuracy and more MOS bias samples. Minimum ~2 (Holt-Winters needs `2×hwSeason`). |
| `FETCH_INTERVAL_MIN` | `.env` | Lower → more frequent retraining/recalibration. 30 min is a good balance. |

---

## Known Limitations

1. **Wind direction bias is uncorrected.** The ECMWF path uses raw wind direction with no local correction, since a circular-aware MOS approach wasn't implemented. If the station's terrain causes a consistent directional deflection, this won't be captured.

2. **Precipitation probability's exact accuracy is unverified.** The amount forecast was backtested directly (see above); the `precipitation_probability` field could only be spot-checked on the live forecast, not backtested at a fixed historical lead time, since Open-Meteo doesn't archive it that way.

3. **Bias correction evidence is from a single ~7-day window, and isn't reproducible from this repo.** The MAE numbers above come from one backtest on this station's available history. A calm week without a frontal passage favors the local model more than a stormy week would — more validation over time (different seasons, different weather regimes) would make the comparison more robust.

4. **Station-only fallback still can't forecast real weather changes.** If Open-Meteo is down for an extended period, the fallback model is a local statistical extrapolation with the same structural ceiling described above — useful as a fallback, not a substitute.

5. **Single-station bias assumption.** The MOS correction assumes the station's offset from the ECMWF grid point is a stable function of hour-of-day. Larger seasonal shifts in that offset (e.g. differs between summer and winter due to local vegetation/shading) aren't modeled separately.

6. **Aurora doesn't use solar wind speed or IMF Bz.** The heuristic uses NOAA's 3-hour Kp forecast only — it can't capture a sudden substorm the Kp forecast didn't predict, and it has no real short-term (sub-hour) nowcasting skill. NOAA's OVATION model (gridded, ~30-70 min lead) would add that, but wasn't integrated — the 3-day Kp forecast was a better fit for this app's multi-day forecast structure.

7. **"Darkness" is sunset-to-sunrise, not true astronomical darkness.** Each hour is judged against the most recent sunrise/sunset event before it, so it is correct across day boundaries. Aurora actually needs the sky to be properly dark (astronomical twilight), not just past sunset. Near the summer solstice in Finland this overestimates the viewing window (it's never fully dark); the rest of the year the approximation is reasonable. A proper twilight calculation would need more than the sunrise/sunset times already available.

8. **Geomagnetic pole coordinates are a fixed approximation.** The dipole pole location drifts ~0.1-0.2°/year; the constant in `aurora.go` isn't kept in sync with that drift. The effect on `requiredKp` is small (a fraction of a degree of geomagnetic latitude) and not worth automating for a single fixed station.

9. **An offline rain module still reads as zero rain.** An unreachable *outdoor* module is detected and its readings are never stored, and a missing or offline *wind* module no longer overwrites stored wind with zero (observations carry which field groups they have). The rain module isn't tracked that way: while it is offline, hours read as dry, which biases the station-only precipitation model until it comes back. The trailing in-progress hour also carries no rain by design (see above), which pulls the model's smoothed recent rate down slightly.

10. **Kp beyond NOAA's published window.** If NOAA's Kp series ends before the 72-hour horizon, the last known value is repeated for the remaining hours (persistence), which is an assumption rather than a forecast.
