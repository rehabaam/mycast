package netatmo

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const baseURL = "https://api.netatmo.com/api"

// Client wraps an authenticated HTTP client for Netatmo API calls.
type Client struct {
	http            *http.Client
	stationID       string
	outdoorModuleID string
	windModuleID    string
	rainModuleID    string

	lat, lon    float64
	hasLocation bool
}

func NewClient(
	httpClient *http.Client,
	stationID, outdoorModuleID, windModuleID, rainModuleID string,
) *Client {
	return &Client{
		http:            httpClient,
		stationID:       stationID,
		outdoorModuleID: outdoorModuleID,
		windModuleID:    windModuleID,
		rainModuleID:    rainModuleID,
	}
}

// GetCurrent fetches live readings for all modules.
func (c *Client) GetCurrent() (*Current, error) {
	params := url.Values{}
	if c.stationID != "" {
		params.Set("device_id", c.stationID)
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
	}

	for _, mod := range dev.Modules {
		d := mod.DashboardData
		switch mod.Type {
		case "NAModule1": // outdoor
			cur.OutdoorTemp = floatVal(d, "Temperature")
			cur.OutdoorHumidity = floatVal(d, "Humidity")
			cur.TodayOutdoorMinC = floatVal(d, "min_temp")
			cur.TodayOutdoorMaxC = floatVal(d, "max_temp")
			cur.TodayOutdoorMinAt = int64(floatVal(d, "date_min_temp"))
			cur.TodayOutdoorMaxAt = int64(floatVal(d, "date_max_temp"))
		case "NAModule2": // wind
			cur.WindSpeed = floatVal(d, "WindStrength")
			cur.WindAngle = floatVal(d, "WindAngle")
			cur.GustSpeed = floatVal(d, "GustStrength")
			cur.GustAngle = floatVal(d, "GustAngle")
		case "NAModule3": // rain
			cur.Rain = floatVal(d, "Rain")
			cur.SumRain1h = floatVal(d, "sum_rain_1")
			cur.SumRain24h = floatVal(d, "sum_rain_24")
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

	cur.ApparentTempC = apparentTemperatureC(cur.OutdoorTemp, cur.OutdoorHumidity, cur.WindSpeed)

	// Auto-discover IDs and location from the API response when not configured.
	if c.stationID == "" {
		c.stationID = dev.ID
	}
	if !c.hasLocation && dev.Place.Location != [2]float64{} {
		c.lon, c.lat = dev.Place.Location[0], dev.Place.Location[1]
		c.hasLocation = true
	}
	for _, mod := range dev.Modules {
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
	}

	return cur, nil
}

// Location returns the station's registered coordinates, discovered from the
// most recent GetCurrent call. ok is false until GetCurrent has succeeded at
// least once.
func (c *Client) Location() (lat, lon float64, ok bool) {
	return c.lat, c.lon, c.hasLocation
}

// GetMeasures fetches hourly historical data for the requested module and types.
// Returns a slice of Observations ordered by ascending timestamp.
func (c *Client) GetMeasures(moduleID string, types []string, from, to time.Time) ([]Observation, error) {
	params := url.Values{
		"device_id":  {c.stationID},
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
			for j, t := range types {
				if j >= len(vals) {
					break
				}
				switch t {
				case "Temperature":
					o.Temperature = vals[j]
				case "Humidity":
					o.Humidity = vals[j]
				case "WindStrength":
					o.WindSpeed = vals[j]
				case "WindAngle":
					o.WindAngle = vals[j]
				case "GustStrength":
					o.GustSpeed = vals[j]
				case "GustAngle":
					o.GustAngle = vals[j]
				case "Rain":
					o.Rain = vals[j]
				}
			}
			obs = append(obs, o)
		}
	}

	return obs, nil
}

// GetHistory fetches all variables for all configured modules over the given period.
func (c *Client) GetHistory(from, to time.Time) ([]Observation, error) {
	outdoorObs, err := c.GetMeasures(c.outdoorModuleID, []string{"Temperature", "Humidity"}, from, to)
	if err != nil {
		return nil, fmt.Errorf("outdoor measures: %w", err)
	}

	// Merge wind and rain into outdoor observations by timestamp.
	index := make(map[int64]*Observation, len(outdoorObs))
	for i := range outdoorObs {
		ts := outdoorObs[i].Timestamp
		index[ts] = &outdoorObs[i]
	}

	if c.windModuleID != "" {
		windObs, err := c.GetMeasures(c.windModuleID, []string{"WindStrength", "WindAngle", "GustStrength", "GustAngle"}, from, to)
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

	if c.rainModuleID != "" {
		rainObs, err := c.GetMeasures(c.rainModuleID, []string{"Rain"}, from, to)
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
	u := fmt.Sprintf("%s/%s?%s", baseURL, endpoint, params.Encode())
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("netatmo %s: HTTP %d — %s", endpoint, resp.StatusCode, body)
	}
	return body, nil
}

func (c *Client) post(endpoint string, params url.Values) ([]byte, error) {
	u := fmt.Sprintf("%s/%s", baseURL, endpoint)
	resp, err := c.http.Post(u, "application/x-www-form-urlencoded", strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("netatmo %s: HTTP %d — %s", endpoint, resp.StatusCode, body)
	}
	return body, nil
}

func joinTypes(types []string) string {
	return strings.Join(types, ",")
}

// apparentTemperatureC estimates "feels like" temperature from air
// temperature, relative humidity, and wind speed, using the standard
// Australian Bureau of Meteorology formula (the same one Open-Meteo
// documents using for its own apparent_temperature field).
func apparentTemperatureC(tempC, humidityPct, windSpeedKmh float64) float64 {
	windMS := windSpeedKmh / 3.6
	vaporPressure := (humidityPct / 100.0) * 6.105 * math.Exp(17.27*tempC/(237.7+tempC))
	return tempC + 0.33*vaporPressure - 0.70*windMS - 4.00
}

func floatVal(d map[string]any, key string) float64 {
	if v, ok := d[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case json.Number:
			f, _ := val.Float64()
			return f
		}
	}
	return 0
}
