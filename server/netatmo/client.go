package netatmo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL = "https://api.netatmo.com/api"

	// maxResponseBytes caps how much of an upstream response is read, so a
	// broken or hostile upstream can't exhaust memory.
	maxResponseBytes = 4 << 20

	// maxErrorBodyBytes caps how much of an error response is echoed into an
	// error message.
	maxErrorBodyBytes = 256
)

// Client wraps an authenticated HTTP client for Netatmo API calls. It is safe
// for concurrent use.
type Client struct {
	http    *http.Client
	baseURL string

	// mu guards everything below: IDs and location are discovered lazily by
	// GetCurrent, which runs from the scheduler, and latest is read by the
	// API handlers.
	mu              sync.RWMutex
	stationID       string
	outdoorModuleID string
	windModuleID    string
	rainModuleID    string

	lat, lon    float64
	hasLocation bool
	timezone    string

	latest *Current
}

func NewClient(
	httpClient *http.Client,
	stationID, outdoorModuleID, windModuleID, rainModuleID string,
) *Client {
	return &Client{
		http:            httpClient,
		baseURL:         defaultBaseURL,
		stationID:       stationID,
		outdoorModuleID: outdoorModuleID,
		windModuleID:    windModuleID,
		rainModuleID:    rainModuleID,
	}
}

// ids returns a consistent snapshot of the configured or discovered IDs.
func (c *Client) ids() (station, outdoor, wind, rain string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stationID, c.outdoorModuleID, c.windModuleID, c.rainModuleID
}

// StationID returns the configured station ID, or the one discovered from the
// most recent GetCurrent call. It is empty until one of those has happened.
func (c *Client) StationID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stationID
}

// Location returns the station's registered coordinates, discovered from the
// most recent GetCurrent call. ok is false until GetCurrent has succeeded at
// least once.
func (c *Client) Location() (lat, lon float64, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lat, c.lon, c.hasLocation
}

// Timezone returns the station's IANA timezone name (e.g. "Europe/Helsinki"),
// or "" if it is not known.
func (c *Client) Timezone() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.timezone
}

// Latest returns a copy of the reading from the most recent successful
// GetCurrent call, without contacting Netatmo. ok is false until one has
// succeeded.
func (c *Client) Latest() (cur *Current, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.latest == nil {
		return nil, false
	}
	return c.latest.clone(), true
}

func (cur *Current) clone() *Current {
	cp := *cur
	cp.Modules = append([]ModuleStatus{}, cur.Modules...)
	return &cp
}

// GetCurrent fetches live readings for all modules and remembers the result
// for Latest.
func (c *Client) GetCurrent() (*Current, error) {
	stationID, outdoorID, windID, rainID := c.ids()

	params := url.Values{}
	if stationID != "" {
		params.Set("device_id", stationID)
	}

	body, err := c.get("getstationsdata", params)
	if err != nil {
		return nil, err
	}

	var resp StationsDataResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse getstationsdata: %w", err)
	}
	if len(resp.Body.Devices) == 0 {
		return nil, fmt.Errorf("no stations found")
	}

	dev := resp.Body.Devices[0]
	cur := &Current{
		Timestamp:      dev.DashboardData.TimeUTC,
		IndoorTemp:     dev.DashboardData.Temperature,
		IndoorHumidity: dev.DashboardData.Humidity,
		Pressure:       dev.DashboardData.Pressure,
		PressureTrend:  dev.DashboardData.PressureTrend,
		TempTrend:      dev.DashboardData.TempTrend,
		Modules:        []ModuleStatus{},
	}

	// The first module of each type that matches its configured ID (if any)
	// supplies the reading, so live readings and history come from the same
	// module.
	var seenOutdoor, seenWind, seenRain bool
	matches := func(mod Module, configured string) bool {
		return configured == "" || mod.ID == configured
	}

	for _, mod := range dev.Modules {
		d := mod.DashboardData
		switch mod.Type {
		case "NAModule1": // outdoor
			if seenOutdoor || !matches(mod, outdoorID) {
				break
			}
			seenOutdoor = true
			temp, okT := floatVal(d, "Temperature")
			hum, okH := floatVal(d, "Humidity")
			if !mod.Reachable || !okT || !okH {
				break
			}
			cur.OutdoorAvailable = true
			cur.OutdoorTemp, cur.OutdoorHumidity = temp, hum
			cur.TodayOutdoorMinC, _ = floatVal(d, "min_temp")
			cur.TodayOutdoorMaxC, _ = floatVal(d, "max_temp")
			minAt, _ := floatVal(d, "date_min_temp")
			maxAt, _ := floatVal(d, "date_max_temp")
			cur.TodayOutdoorMinAt, cur.TodayOutdoorMaxAt = int64(minAt), int64(maxAt)
		case "NAModule2": // wind
			if seenWind || !matches(mod, windID) {
				break
			}
			seenWind = true
			cur.WindSpeed, _ = floatVal(d, "WindStrength")
			cur.WindAngle, _ = floatVal(d, "WindAngle")
			cur.GustSpeed, _ = floatVal(d, "GustStrength")
			cur.GustAngle, _ = floatVal(d, "GustAngle")
		case "NAModule3": // rain
			if seenRain || !matches(mod, rainID) {
				break
			}
			seenRain = true
			cur.Rain, _ = floatVal(d, "Rain")
			cur.SumRain1h, _ = floatVal(d, "sum_rain_1")
			cur.SumRain24h, _ = floatVal(d, "sum_rain_24")
		}

		cur.Modules = append(cur.Modules, ModuleStatus{
			Type:           mod.Type,
			Name:           mod.ModuleName,
			BatteryPercent: mod.BatteryPercent,
			Reachable:      mod.Reachable,
			RFStatus:       mod.RFStatus,
			LastSeen:       mod.LastSeen,
		})
	}

	// Auto-discover IDs, location and timezone from the API response when not
	// configured.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stationID == "" {
		c.stationID = dev.ID
	}
	if !c.hasLocation && dev.Place.Location != [2]float64{} {
		c.lon, c.lat = dev.Place.Location[0], dev.Place.Location[1]
		c.hasLocation = true
	}
	if c.timezone == "" {
		c.timezone = dev.Place.Timezone
	}
	discovered := map[string]bool{}
	for _, mod := range dev.Modules {
		if discovered[mod.Type] {
			continue
		}
		switch mod.Type {
		case "NAModule1":
			if c.outdoorModuleID == "" {
				c.outdoorModuleID = mod.ID
			}
		case "NAModule2":
			if c.windModuleID == "" {
				c.windModuleID = mod.ID
			}
		case "NAModule3":
			if c.rainModuleID == "" {
				c.rainModuleID = mod.ID
			}
		}
		discovered[mod.Type] = true
	}

	c.latest = cur.clone()
	return cur, nil
}

// GetMeasures fetches hourly historical data for the requested module and types.
// Returns a slice of Observations ordered by ascending timestamp. Steps where
// the API reports no temperature or humidity are dropped rather than stored
// as zeros.
func (c *Client) GetMeasures(moduleID string, types []string, from, to time.Time) ([]Observation, error) {
	stationID, _, _, _ := c.ids()
	params := url.Values{
		"device_id":  {stationID},
		"scale":      {"1hour"},
		"type":       {joinTypes(types)},
		"date_begin": {strconv.FormatInt(from.Unix(), 10)},
		"date_end":   {strconv.FormatInt(to.Unix(), 10)},
		"optimize":   {"true"},
		"real_time":  {"false"},
	}
	if moduleID != "" {
		params.Set("module_id", moduleID)
	}

	body, err := c.post("getmeasure", params)
	if err != nil {
		return nil, err
	}

	var resp MeasureResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse getmeasure: %w", err)
	}

	var obs []Observation
	for _, seg := range resp.Body {
		for i, vals := range seg.Value {
			ts := seg.BegTime + int64(i)*seg.StepTime
			o := Observation{Timestamp: ts}
			complete := true
			for j, t := range types {
				if j >= len(vals) {
					break
				}
				v := vals[j]
				if v == nil {
					if t == "Temperature" || t == "Humidity" {
						complete = false
					}
					continue
				}
				switch t {
				case "Temperature":
					o.Temperature = *v
				case "Humidity":
					o.Humidity = *v
				case "WindStrength":
					o.WindSpeed = *v
				case "WindAngle":
					o.WindAngle = *v
				case "GustStrength":
					o.GustSpeed = *v
				case "GustAngle":
					o.GustAngle = *v
				case "Rain":
					o.Rain = *v
				}
			}
			if complete {
				obs = append(obs, o)
			}
		}
	}

	return obs, nil
}

// GetHistory fetches all variables for all configured modules over the given period.
func (c *Client) GetHistory(from, to time.Time) ([]Observation, error) {
	_, outdoorID, windID, rainID := c.ids()

	outdoorObs, err := c.GetMeasures(outdoorID, []string{"Temperature", "Humidity"}, from, to)
	if err != nil {
		return nil, fmt.Errorf("outdoor measures: %w", err)
	}

	// Merge wind and rain into outdoor observations by timestamp.
	index := make(map[int64]*Observation, len(outdoorObs))
	for i := range outdoorObs {
		ts := outdoorObs[i].Timestamp
		index[ts] = &outdoorObs[i]
	}

	if windID != "" {
		windObs, err := c.GetMeasures(windID, []string{"WindStrength", "WindAngle", "GustStrength", "GustAngle"}, from, to)
		if err != nil {
			return nil, fmt.Errorf("wind measures: %w", err)
		}
		for _, w := range windObs {
			if o, ok := index[w.Timestamp]; ok {
				o.WindSpeed = w.WindSpeed
				o.WindAngle = w.WindAngle
				o.GustSpeed = w.GustSpeed
				o.GustAngle = w.GustAngle
			}
		}
	}

	if rainID != "" {
		rainObs, err := c.GetMeasures(rainID, []string{"Rain"}, from, to)
		if err != nil {
			return nil, fmt.Errorf("rain measures: %w", err)
		}
		for _, r := range rainObs {
			if o, ok := index[r.Timestamp]; ok {
				o.Rain = r.Rain
			}
		}
	}

	return outdoorObs, nil
}

func (c *Client) get(endpoint string, params url.Values) ([]byte, error) {
	u := fmt.Sprintf("%s/%s?%s", c.baseURL, endpoint, params.Encode())
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	return readResponse(endpoint, resp)
}

func (c *Client) post(endpoint string, params url.Values) ([]byte, error) {
	u := fmt.Sprintf("%s/%s", c.baseURL, endpoint)
	resp, err := c.http.Post(u, "application/x-www-form-urlencoded", strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	return readResponse(endpoint, resp)
}

// readResponse reads at most maxResponseBytes of the body and turns non-200
// statuses into errors.
func readResponse(endpoint string, resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body %s: %w", endpoint, err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("netatmo %s: response exceeds %d bytes", endpoint, maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := body
		if len(snippet) > maxErrorBodyBytes {
			snippet = snippet[:maxErrorBodyBytes]
		}
		return nil, fmt.Errorf("netatmo %s: HTTP %d — %s", endpoint, resp.StatusCode, snippet)
	}
	return body, nil
}

func joinTypes(types []string) string {
	return strings.Join(types, ",")
}

// floatVal reads a numeric field from a module's dashboard data. ok is false
// when the key is missing or not a number, so callers can tell "absent" from
// a genuine zero.
func floatVal(d map[string]any, key string) (val float64, ok bool) {
	switch v := d[key].(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}
