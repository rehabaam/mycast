package forecast

import (
	"testing"
	"time"
)

func flatSlice(n int, v float64) []float64 {
	s := make([]float64, n)
	for i := range s {
		s[i] = v
	}
	return s
}

// flatDayInput is a dayInput with constant weather and no optional data.
func flatDayInput(now time.Time, loc *time.Location) dayInput {
	return dayInput{
		now:    now,
		loc:    loc,
		temps:  flatSlice(forecastHours, 10.0),
		humid:  flatSlice(forecastHours, 80.0),
		speeds: flatSlice(forecastHours, 5.0),
		angles: flatSlice(forecastHours, 180.0),
		precip: precipResult{amounts: flatSlice(forecastHours, 0.5), probabilities: flatSlice(forecastHours, 0.6)},
	}
}

// dailySun returns sunrise/sunset instants at the given UTC hours for each
// date from `from` for `days` days.
func dailySun(from time.Time, days, riseHour, setHour int) (sunrises, sunsets []time.Time) {
	day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < days; i++ {
		d := day.AddDate(0, 0, i)
		sunrises = append(sunrises, d.Add(time.Duration(riseHour)*time.Hour))
		sunsets = append(sunsets, d.Add(time.Duration(setHour)*time.Hour))
	}
	return sunrises, sunsets
}

func hourCounts(days []DayForecast) []int {
	out := make([]int, len(days))
	for i, d := range days {
		out[i] = len(d.Temperature.Hourly)
	}
	return out
}

func TestBuildDaysWithConditionCodes(t *testing.T) {
	in := flatDayInput(time.Date(2026, time.June, 15, 10, 30, 0, 0, time.UTC), time.UTC)
	in.codes = make([]int, forecastHours)
	for i := range in.codes {
		in.codes[i] = 61 // "Slight rain"
	}
	in.sunrises, in.sunsets = dailySun(time.Date(2026, time.June, 14, 0, 0, 0, 0, time.UTC), 6, 4, 20)

	// Kp well above the test location's required threshold, clear skies, so
	// the dark hours (before sunrise / after sunset) should show a real
	// chance.
	in.cloudCover = flatSlice(forecastHours, 0.0)
	in.kp = flatSlice(forecastHours, 7.0)
	in.geomagLat = geomagneticLatitude(fairbanksLat, fairbanksLon)

	days := buildDays(in)
	if len(days) != 3 {
		t.Fatalf("expected 3 days, got %d", len(days))
	}

	// 11:00 start: 13 hours left today, then two full days; the remaining
	// 11 hours would fall on a fourth day and are dropped.
	if got, want := hourCounts(days), []int{13, 24, 24}; !equalInts(got, want) {
		t.Errorf("hours per day = %v, want %v", got, want)
	}
	for i, want := range []struct{ date, weekday string }{
		{"2026-06-15", "Monday"}, {"2026-06-16", "Tuesday"}, {"2026-06-17", "Wednesday"},
	} {
		if days[i].Date != want.date || days[i].DayOfWeek != want.weekday {
			t.Errorf("Days[%d] = %s %s, want %s %s", i, days[i].Date, days[i].DayOfWeek, want.date, want.weekday)
		}
	}

	day := days[0]
	if day.Condition.Summary != "Slight rain" {
		t.Errorf("Condition.Summary = %q, want %q", day.Condition.Summary, "Slight rain")
	}
	if len(day.Condition.Hourly) == 0 {
		t.Fatal("expected hourly condition data")
	}
	if day.Condition.Hourly[0].Code != 61 {
		t.Errorf("Condition.Hourly[0].Code = %d, want 61", day.Condition.Hourly[0].Code)
	}

	// Each day reports the sunrise/sunset that happen on that date.
	for i, dom := range []int{15, 16, 17} {
		wantRise := time.Date(2026, time.June, dom, 4, 0, 0, 0, time.UTC)
		wantSet := time.Date(2026, time.June, dom, 20, 0, 0, 0, time.UTC)
		if days[i].Sunrise == nil || !days[i].Sunrise.Equal(wantRise) {
			t.Errorf("Days[%d].Sunrise = %v, want %v", i, days[i].Sunrise, wantRise)
		}
		if days[i].Sunset == nil || !days[i].Sunset.Equal(wantSet) {
			t.Errorf("Days[%d].Sunset = %v, want %v", i, days[i].Sunset, wantSet)
		}
	}

	if day.Aurora == nil {
		t.Fatal("expected Aurora to be populated when cloudCover/kp are provided")
	}
	if day.Aurora.MaxProbabilityPct <= 0 {
		t.Errorf("Aurora.MaxProbabilityPct = %.2f, want > 0 (high Kp, clear skies, dark hours present)", day.Aurora.MaxProbabilityPct)
	}
	if len(day.Aurora.Hourly) != len(day.Temperature.Hourly) {
		t.Errorf("Aurora.Hourly length (%d) != Hourly length (%d)", len(day.Aurora.Hourly), len(day.Temperature.Hourly))
	}

	// Darkness is judged against each hour's own sunrise/sunset, so the
	// second day's midday is daylight, not "after yesterday's sunset".
	for di, d := range days {
		for _, h := range d.Aurora.Hourly {
			hr := h.Time.UTC().Hour()
			if wantDark := hr < 4 || hr >= 20; h.IsDark != wantDark {
				t.Errorf("Days[%d] %v: IsDark = %v, want %v", di, h.Time, h.IsDark, wantDark)
			}
		}
	}

	// Apparent temp at 10°C/80%/5kmh should be a real, sane value distinct
	// from raw temp, and populated for every hour.
	if len(day.Temperature.ApparentHourly) != len(day.Temperature.Hourly) {
		t.Fatalf("ApparentHourly length (%d) != Hourly length (%d)", len(day.Temperature.ApparentHourly), len(day.Temperature.Hourly))
	}
	if day.Temperature.ApparentAvgC == 0 {
		t.Error("ApparentAvgC should be populated, got 0")
	}
}

func TestBuildDaysBucketsByLocalCalendarDay(t *testing.T) {
	eest := time.FixedZone("EEST", 3*3600)

	t.Run("starts exactly at local midnight", func(t *testing.T) {
		// 20:30 UTC is 23:30 local, so the first forecast hour (21:00 UTC)
		// is local midnight and the first day is the whole of Jun 16.
		days := buildDays(flatDayInput(time.Date(2026, time.June, 15, 20, 30, 0, 0, time.UTC), eest))
		if got, want := hourCounts(days), []int{24, 24, 24}; !equalInts(got, want) {
			t.Errorf("hours per day = %v, want %v", got, want)
		}
		if days[0].Date != "2026-06-16" || days[0].DayOfWeek != "Tuesday" {
			t.Errorf("first day = %s %s, want 2026-06-16 Tuesday", days[0].Date, days[0].DayOfWeek)
		}
	})

	t.Run("partial first day", func(t *testing.T) {
		// 13:30 local: the first forecast hour is 14:00, leaving 10 hours.
		days := buildDays(flatDayInput(time.Date(2026, time.June, 15, 10, 30, 0, 0, time.UTC), eest))
		if got, want := hourCounts(days), []int{10, 24, 24}; !equalInts(got, want) {
			t.Errorf("hours per day = %v, want %v", got, want)
		}
		if days[0].Date != "2026-06-15" {
			t.Errorf("first day = %s, want 2026-06-15", days[0].Date)
		}
	})

	t.Run("single hour left today", func(t *testing.T) {
		// 22:30 UTC with UTC days: only 23:00 remains of today.
		days := buildDays(flatDayInput(time.Date(2026, time.June, 15, 22, 30, 0, 0, time.UTC), time.UTC))
		if got, want := hourCounts(days), []int{1, 24, 24}; !equalInts(got, want) {
			t.Errorf("hours per day = %v, want %v", got, want)
		}
		d := days[0]
		if d.Temperature.MinC != 10 || d.Temperature.MaxC != 10 || d.Temperature.AvgC != 10 {
			t.Errorf("single-hour day temperature = %+v, want all 10", d.Temperature)
		}
	})

	t.Run("daily aggregates only cover the day's own hours", func(t *testing.T) {
		in := flatDayInput(time.Date(2026, time.June, 15, 10, 30, 0, 0, time.UTC), time.UTC)
		// Today (13 hours) is 5 °C, tomorrow 15 °C, the day after 25 °C.
		for i := range in.temps {
			switch {
			case i < 13:
				in.temps[i] = 5
			case i < 37:
				in.temps[i] = 15
			default:
				in.temps[i] = 25
			}
		}
		days := buildDays(in)
		for i, want := range []float64{5, 15, 25} {
			tmp := days[i].Temperature
			if tmp.MinC != want || tmp.MaxC != want || tmp.AvgC != want {
				t.Errorf("Days[%d] min/max/avg = %v/%v/%v, want all %v", i, tmp.MinC, tmp.MaxC, tmp.AvgC, want)
			}
		}
	})
}

func TestBuildDaysSummaryUsesLocalMidday(t *testing.T) {
	eest := time.FixedZone("EEST", 3*3600)

	t.Run("full days use the local-noon hour", func(t *testing.T) {
		// 06:30 UTC → first hour 07:00 UTC. Local noon is 09:00 UTC, i.e.
		// hour index 2 of today and 24 and 48 hours later.
		in := flatDayInput(time.Date(2026, time.June, 15, 6, 30, 0, 0, time.UTC), eest)
		in.codes = make([]int, forecastHours)
		for i := range in.codes {
			in.codes[i] = 3 // "Overcast"
		}
		for _, idx := range []int{2, 26, 50} {
			in.codes[idx] = 61
		}
		for i, day := range buildDays(in) {
			if day.Condition.Summary != "Slight rain" {
				t.Errorf("Days[%d] summary = %q, want %q (the condition at local noon)", i, day.Condition.Summary, "Slight rain")
			}
		}
	})

	t.Run("a day already past noon uses its first hour", func(t *testing.T) {
		// First hour is 14:00 local, so today has no noon: the closest hour
		// is the first one.
		in := flatDayInput(time.Date(2026, time.June, 15, 10, 30, 0, 0, time.UTC), eest)
		in.codes = make([]int, forecastHours)
		for i := range in.codes {
			in.codes[i] = 3
		}
		in.codes[0] = 61
		if got := buildDays(in)[0].Condition.Summary; got != "Slight rain" {
			t.Errorf("today's summary = %q, want %q", got, "Slight rain")
		}
	})
}

func TestBuildDaysWithNilCodesDegradesGracefully(t *testing.T) {
	days := buildDays(flatDayInput(testNow, time.UTC))

	day := days[0]
	if day.Condition.Summary != "Unknown" {
		t.Errorf("Condition.Summary = %q, want %q (nil codes)", day.Condition.Summary, "Unknown")
	}
	for _, h := range day.Condition.Hourly {
		if h.Summary != "Unknown" {
			t.Errorf("hourly Summary = %q, want %q (nil codes)", h.Summary, "Unknown")
		}
	}
	if day.Sunrise != nil {
		t.Errorf("Sunrise = %v, want nil (no daily data)", day.Sunrise)
	}
	if day.Sunset != nil {
		t.Errorf("Sunset = %v, want nil (no daily data)", day.Sunset)
	}
	if day.Aurora != nil {
		t.Errorf("Aurora = %v, want nil (no cloud cover/Kp data)", day.Aurora)
	}
}

func TestIsDarkAt(t *testing.T) {
	at := func(day, hour int) time.Time {
		return time.Date(2026, time.June, day, hour, 0, 0, 0, time.UTC)
	}
	sunrises := []time.Time{at(15, 4), at(16, 4)}
	sunsets := []time.Time{at(14, 20), at(15, 20)}

	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"before dawn, after yesterday's sunset", at(15, 3), true},
		{"at sunrise", at(15, 4), false},
		{"midday", at(15, 12), false},
		{"at sunset", at(15, 20), true},
		{"late evening", at(15, 22), true},
		{"midday the next day, past every sunset on file", at(16, 12), false},
		{"before the first event, which is a sunset", at(13, 12), false},
	}
	for _, c := range cases {
		if got := isDarkAt(c.t, sunrises, sunsets); got != c.want {
			t.Errorf("%s: isDarkAt(%v) = %v, want %v", c.name, c.t, got, c.want)
		}
	}

	if !isDarkAt(at(13, 12), []time.Time{at(14, 4)}, nil) {
		t.Error("before the first event, which is a sunrise, it should be dark")
	}
	if isDarkAt(at(15, 12), nil, nil) {
		t.Error("with no sun data nothing should count as dark")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
