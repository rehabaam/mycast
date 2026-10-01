package netatmo

// StationsDataResponse is returned by GET /api/getstationsdata.
type StationsDataResponse struct {
	Body   StationsBody `json:"body"`
	Status string       `json:"status"`
}

type StationsBody struct {
	Devices []Device `json:"devices"`
}

type Device struct {
	ID            string          `json:"_id"`
	StationName   string          `json:"station_name"`
	DashboardData IndoorDashboard `json:"dashboard_data"`
	Modules       []Module        `json:"modules"`
	Place         Place           `json:"place"`
}

// Place holds the station's registered location, used to query a gridded
// NWP forecast (e.g. Open-Meteo/ECMWF) for the exact coordinates.
type Place struct {
	Location [2]float64 `json:"location"` // [longitude, latitude]
	City     string     `json:"city"`
	Country  string     `json:"country"`
	Altitude float64    `json:"altitude"`
	Timezone string     `json:"timezone"` // IANA name, e.g. "Europe/Helsinki"
}

type IndoorDashboard struct {
	TimeUTC          int64   `json:"time_utc"`
	Temperature      float64 `json:"Temperature"`
	CO2              float64 `json:"CO2"`
	Humidity         float64 `json:"Humidity"`
	Noise            float64 `json:"Noise"`
	Pressure         float64 `json:"Pressure"`
	AbsolutePressure float64 `json:"AbsolutePressure"`
	TempTrend        string  `json:"temp_trend"`
	PressureTrend    string  `json:"pressure_trend"`
}

type Module struct {
	ID             string         `json:"_id"`
	Type           string         `json:"type"` // NAModule1=outdoor, NAModule2=wind, NAModule3=rain, NAModule4=additional indoor
	ModuleName     string         `json:"module_name"`
	BatteryPercent int            `json:"battery_percent"`
	Reachable      bool           `json:"reachable"`
	RFStatus       int            `json:"rf_status"`
	LastSeen       int64          `json:"last_seen"`
	DashboardData  map[string]any `json:"dashboard_data"`
}

// MeasureResponse is returned by GET /api/getmeasure with optimize=true.
type MeasureResponse struct {
	Body   []MeasureBody `json:"body"`
	Status string        `json:"status"`
}

type MeasureBody struct {
	BegTime  int64        `json:"beg_time"`
	StepTime int64        `json:"step_time"`
	Value    [][]*float64 `json:"value"` // nil entries are steps with no data
}

// Observation holds a single point-in-time reading across all station variables.
type Observation struct {
	Timestamp   int64
	Temperature float64 // °C (outdoor)
	Humidity    float64 // % (outdoor)
	WindSpeed   float64 // km/h
	WindAngle   float64 // degrees 0-360
	GustSpeed   float64 // km/h
	GustAngle   float64 // degrees 0-360
	Rain        float64 // mm accumulated over the hour
}

// Current holds the latest live readings from the station.
type Current struct {
	Timestamp int64

	// OutdoorAvailable is false when the outdoor module is missing,
	// unreachable, or reported no temperature/humidity. The outdoor fields
	// below are then zero and must not be treated as a real reading.
	OutdoorAvailable bool

	OutdoorTemp     float64
	OutdoorHumidity float64
	IndoorTemp      float64
	IndoorHumidity  float64
	Pressure        float64
	PressureTrend   string // "up", "down", "stable" — computed by Netatmo
	TempTrend       string
	WindSpeed       float64
	WindAngle       float64
	GustSpeed       float64
	GustAngle       float64
	Rain            float64
	SumRain1h       float64
	SumRain24h      float64

	// TodayOutdoorMinC/MaxC are the actual recorded outdoor extremes for
	// today (as tracked by the station itself), distinct from any forecast.
	TodayOutdoorMinC  float64
	TodayOutdoorMaxC  float64
	TodayOutdoorMinAt int64
	TodayOutdoorMaxAt int64

	// Modules reports connectivity/battery health for every station module.
	Modules []ModuleStatus
}

// ModuleStatus reports a single module's connectivity and battery health.
type ModuleStatus struct {
	Type           string
	Name           string
	BatteryPercent int
	Reachable      bool
	RFStatus       int
	LastSeen       int64
}

// Observation converts the live reading into a time-series Observation. ok is
// false when the outdoor module has no usable reading, in which case the
// returned Observation must not be stored: its zero temperature/humidity
// would be indistinguishable from a real 0 °C / 0 % reading. Rain uses the
// rolling one-hour sum so it matches the units of the hourly history.
func (c *Current) Observation() (obs Observation, ok bool) {
	if !c.OutdoorAvailable {
		return Observation{}, false
	}
	return Observation{
		Timestamp:   c.Timestamp,
		Temperature: c.OutdoorTemp,
		Humidity:    c.OutdoorHumidity,
		WindSpeed:   c.WindSpeed,
		WindAngle:   c.WindAngle,
		GustSpeed:   c.GustSpeed,
		GustAngle:   c.GustAngle,
		Rain:        c.SumRain1h,
	}, true
}
