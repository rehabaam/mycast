package netatmo

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// Config holds the station and module IDs to use instead of auto-discovered
// ones. Any may be empty.
type Config struct {
	StationID       string
	OutdoorModuleID string
	WindModuleID    string
	RainModuleID    string
}

// Client wraps an authenticated HTTP client for Netatmo API calls.
//
// It holds no state of its own beyond its configuration, so it is safe for
// concurrent use and its methods can be called in any order. What it learns
// about the station comes back as a Station value (see Current.Station),
// which history calls take as an argument: where to cache a reading, and for
// how long it is trusted, is the caller's decision.
type Client struct {
	http    *http.Client
	baseURL string
	cfg     Config
}

func NewClient(httpClient *http.Client, cfg Config) *Client {
	return &Client{http: httpClient, baseURL: defaultBaseURL, cfg: cfg}
}

// discoverStation resolves the station's identity: configured values win, and
// anything left blank is taken from the response.
func discoverStation(cfg Config, dev Device) Station {
	st := Station{
		ID:              firstNonEmpty(cfg.StationID, dev.ID),
		OutdoorModuleID: cfg.OutdoorModuleID,
		WindModuleID:    cfg.WindModuleID,
		RainModuleID:    cfg.RainModuleID,
		Timezone:        dev.Place.Timezone,
	}
	if dev.Place.Location != [2]float64{} {
		st.Lon, st.Lat = dev.Place.Location[0], dev.Place.Location[1]
		st.HasLocation = true
	}

	// For each module type, the first module in the response fills a blank.
	for _, mod := range dev.Modules {
		switch mod.Type {
		case "NAModule1":
			if st.OutdoorModuleID == "" {
				st.OutdoorModuleID = mod.ID
			}
		case "NAModule2":
			if st.WindModuleID == "" {
				st.WindModuleID = mod.ID
			}
		case "NAModule3":
			if st.RainModuleID == "" {
				st.RainModuleID = mod.ID
			}
		}
	}
	return st
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// GetCurrent fetches live readings for all modules.
func (c *Client) GetCurrent() (*Current, error) {
	params := url.Values{}
	if c.cfg.StationID != "" {
		params.Set("device_id", c.cfg.StationID)
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
	st := discoverStation(c.cfg, dev)
	cur := &Current{
		FetchedAt:      time.Now(),
		Station:        st,
		Timestamp:      dev.DashboardData.TimeUTC,
		IndoorTemp:     dev.DashboardData.Temperature,
		IndoorHumidity: dev.DashboardData.Humidity,
		Pressure:       dev.DashboardData.Pressure,
		PressureTrend:  dev.DashboardData.PressureTrend,
		TempTrend:      dev.DashboardData.TempTrend,
		Modules:        []ModuleStatus{},
	}

	for _, mod := range dev.Modules {
		d := mod.DashboardData
		switch mod.Type {
		case "NAModule1": // outdoor
			if mod.ID != st.OutdoorModuleID {
				break
			}
			temp, okT := floatVal(d, "Temperature")
			hum, okH := floatVal(d, "Humidity")
			// The module's own measurement time. Falling back to when the
			// base last heard from it still dates the reading to the module,
			// not to the base's own clock.
			at, okAt := floatVal(d, "time_utc")
			if !okAt && mod.LastSeen > 0 {
				at, okAt = float64(mod.LastSeen), true
			}
			if !mod.Reachable || !okT || !okH || !okAt {
				break
			}
			cur.OutdoorAvailable = true
			cur.OutdoorTimestamp = int64(at)
			cur.OutdoorTemp, cur.OutdoorHumidity = temp, hum
			cur.TodayOutdoorMinC, _ = floatVal(d, "min_temp")
			cur.TodayOutdoorMaxC, _ = floatVal(d, "max_temp")
			minAt, _ := floatVal(d, "date_min_temp")
			maxAt, _ := floatVal(d, "date_max_temp")
			cur.TodayOutdoorMinAt, cur.TodayOutdoorMaxAt = int64(minAt), int64(maxAt)
		case "NAModule2": // wind
			if mod.ID != st.WindModuleID {
				break
			}
			speed, okS := floatVal(d, "WindStrength")
			angle, okA := floatVal(d, "WindAngle")
			if !mod.Reachable || !okS || !okA {
				break
			}
			cur.WindAvailable = true
			cur.WindSpeed, cur.WindAngle = speed, angle
			cur.GustSpeed, _ = floatVal(d, "GustStrength")
			cur.GustAngle, _ = floatVal(d, "GustAngle")
		case "NAModule3": // rain
			if mod.ID != st.RainModuleID {
				break
			}
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

	return cur, nil
}

// measureGroup maps a getmeasure type to the group of fields it belongs to.
var measureGroup = map[string]Fields{
	"Temperature":  FieldOutdoor,
	"Humidity":     FieldOutdoor,
	"WindStrength": FieldWind,
	"WindAngle":    FieldWind,
	"GustStrength": FieldWind,
	"GustAngle":    FieldWind,
	"Rain":         FieldRain,
}

// GetMeasures fetches hourly historical data for one module of a station.
// Returns Observations ordered by ascending timestamp.
//
// A group of requested types counts as present (Observation.Has) only if the
// API reported a value for every type in it. Steps where no group is present
// are dropped, so a gap in Netatmo's data is never turned into zeros, and a
// caller can tell the groups it did get apart from the ones it didn't.
func (c *Client) GetMeasures(deviceID, moduleID string, types []string, from, to time.Time) ([]Observation, error) {
	params := url.Values{
		"device_id":  {deviceID},
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
			o := Observation{Timestamp: seg.BegTime + int64(i)*seg.StepTime}
			var requested, missing Fields
			for j, t := range types {
				group := measureGroup[t]
				requested |= group

				var v *float64
				if j < len(vals) {
					v = vals[j]
				}
				if v == nil {
					missing |= group
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
			if o.Has = requested &^ missing; o.Has != 0 {
				obs = append(obs, o)
			}
		}
	}

	return obs, nil
}

// GetHistory fetches hourly history for the station's modules over the given
// period. Every observation says which of its field groups Netatmo actually
// supplied (Has): an hour with temperature but no wind aggregate has
// FieldOutdoor only, rather than a made-up calm.
func (c *Client) GetHistory(st Station, from, to time.Time) ([]Observation, error) {
	if st.OutdoorModuleID == "" {
		// Without a module ID getmeasure answers for the indoor base station,
		// which would then be stored as outdoor history.
		return nil, errors.New("station has no outdoor module")
	}

	outdoorObs, err := c.GetMeasures(st.ID, st.OutdoorModuleID, []string{"Temperature", "Humidity"}, from, to)
	if err != nil {
		return nil, fmt.Errorf("outdoor measures: %w", err)
	}

	// Merge wind and rain into outdoor observations by timestamp.
	index := make(map[int64]*Observation, len(outdoorObs))
	for i := range outdoorObs {
		index[outdoorObs[i].Timestamp] = &outdoorObs[i]
	}

	if st.WindModuleID != "" {
		windObs, err := c.GetMeasures(st.ID, st.WindModuleID, []string{"WindStrength", "WindAngle", "GustStrength", "GustAngle"}, from, to)
		if err != nil {
			return nil, fmt.Errorf("wind measures: %w", err)
		}
		// GetMeasures only returns steps whose group was fully reported, so
		// every row here is a real wind measurement.
		for _, w := range windObs {
			if o, ok := index[w.Timestamp]; ok {
				o.WindSpeed, o.WindAngle = w.WindSpeed, w.WindAngle
				o.GustSpeed, o.GustAngle = w.GustSpeed, w.GustAngle
				o.Has |= FieldWind
			}
		}
	}

	if st.RainModuleID != "" {
		rainObs, err := c.GetMeasures(st.ID, st.RainModuleID, []string{"Rain"}, from, to)
		if err != nil {
			return nil, fmt.Errorf("rain measures: %w", err)
		}
		for _, r := range rainObs {
			if o, ok := index[r.Timestamp]; ok {
				o.Rain = r.Rain
				o.Has |= FieldRain
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
