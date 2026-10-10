package netatmo

import "time"

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

// Fields names the groups of measurements an Observation actually carries. A
// module that is offline, or an hour Netatmo has no aggregate for, leaves its
// group at zero in the struct, and zero is a perfectly good wind speed: this
// is what tells "calm" apart from "no data" when readings are merged.
type Fields uint8

const (
	FieldOutdoor Fields = 1 << iota // Temperature and Humidity
	FieldWind                       // WindSpeed, WindAngle, GustSpeed, GustAngle
	FieldRain                       // Rain

	FieldsAll = FieldOutdoor | FieldWind | FieldRain
)

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

	// Has lists the field groups that were really measured. The zero value
	// means every group is present, so a plain Observation literal is a
	// complete reading; the Netatmo client sets it explicitly on anything
	// partial.
	Has Fields
}

// Provides returns the field groups this observation carries.
func (o Observation) Provides() Fields {
	if o.Has == 0 {
		return FieldsAll
	}
	return o.Has
}

// Station is what Netatmo reports about the station itself: its identity,
// the modules to read, and where it is. It is derived from a single
// getstationsdata response plus the configured overrides, so it is a plain
// value rather than state the client carries around.
type Station struct {
	ID string

	// Module IDs are the configured ones, or else the first module of each
	// type in the response. Empty means the station has no such module.
	OutdoorModuleID string
	WindModuleID    string
	RainModuleID    string

	Lat, Lon    float64
	HasLocation bool

	// Timezone is the IANA name (e.g. "Europe/Helsinki"), or "" if unknown.
	Timezone string
}

// Current holds the latest live readings from the station.
type Current struct {
	// Timestamp is when Netatmo says the indoor base station measured its
	// part of this reading (see OutdoorTimestamp for the outdoor module).
	Timestamp int64

	// FetchedAt is when this service successfully retrieved it. A reading
	// that keeps being served long after FetchedAt means fetches are failing.
	FetchedAt time.Time

	// Station identifies the station and its modules, as seen in this
	// response.
	Station Station

	// OutdoorAvailable is false when the outdoor module is missing,
	// unreachable, or reported no temperature/humidity or measurement time.
	// The outdoor fields below are then zero and must not be treated as a
	// real reading.
	OutdoorAvailable bool

	// OutdoorTimestamp is when the outdoor module itself took its
	// measurement. It can be much older than Timestamp, which belongs to the
	// indoor base station: a module that has gone quiet leaves its last
	// values in place while the base keeps ticking.
	OutdoorTimestamp int64

	// WindAvailable is false when the wind module is missing or unreachable
	// or reported no wind; the wind fields below are then zero.
	WindAvailable bool

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

// Clone returns a copy that shares no memory with c.
func (c *Current) Clone() *Current {
	cp := *c
	cp.Modules = append([]ModuleStatus{}, c.Modules...)
	return &cp
}

// Observation converts the live reading into a time-series Observation. ok is
// false when the outdoor module has no usable reading, in which case the
// returned Observation must not be stored: its zero temperature/humidity
// would be indistinguishable from a real 0 °C / 0 % reading.
//
// The observation is stamped with the outdoor module's own measurement time,
// not the base station's. A module that has gone quiet but is still flagged
// reachable keeps its last values on the dashboard while the base keeps
// ticking; stamping those values with the base's clock would file stale data
// under fresh hours.
//
// Wind is included only when the wind module reported (see Has), so an
// offline wind module leaves whatever wind is already stored for the hour
// instead of overwriting it with zero.
//
// Rain is left out on purpose. The station only reports a rolling last-hour
// sum (SumRain1h), which at 10:05 covers 09:05-10:05; stored in the 10:00
// bucket it would overlap the 09:00 bucket's clock-hour total and count that
// rain twice. The hour's real total comes from Netatmo's hourly history,
// which the scheduler re-reads for completed hours.
func (c *Current) Observation() (obs Observation, ok bool) {
	if !c.OutdoorAvailable {
		return Observation{}, false
	}
	has := FieldOutdoor
	if c.WindAvailable {
		has |= FieldWind
	}
	return Observation{
		Timestamp:   c.OutdoorTimestamp,
		Temperature: c.OutdoorTemp,
		Humidity:    c.OutdoorHumidity,
		WindSpeed:   c.WindSpeed,
		WindAngle:   c.WindAngle,
		GustSpeed:   c.GustSpeed,
		GustAngle:   c.GustAngle,
		Has:         has,
	}, true
}
