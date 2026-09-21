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

func TestBuildDaysWithConditionCodes(t *testing.T) {
	temps := flatSlice(forecastHours, 10.0)
	humid := flatSlice(forecastHours, 80.0)
	speeds := flatSlice(forecastHours, 5.0)
	angles := flatSlice(forecastHours, 180.0)
	codes := make([]int, forecastHours)
	for i := range codes {
		codes[i] = 61 // "Slight rain"
	}
	precip := precipResult{amounts: flatSlice(forecastHours, 0.5), probabilities: flatSlice(forecastHours, 0.6)}

	start := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	dateStr := start.Format("2006-01-02")
	wantSunrise := start.Add(6 * time.Hour)
	wantSunset := start.Add(18 * time.Hour)
	sunriseByDate := map[string]time.Time{dateStr: wantSunrise}
	sunsetByDate := map[string]time.Time{dateStr: wantSunset}

	// Kp well above the test location's required threshold, clear skies, so
	// the dark hours (before sunrise / after sunset) should show a real
	// chance. Fairbanks, Alaska: a public, well-documented aurora reference
	// point (~65.6° geomagnetic latitude, needs only Kp ~1-2).
	cloudCover := flatSlice(forecastHours, 0.0)
	kpHourly := flatSlice(forecastHours, 7.0)
	const testLat, testLon = 64.84, -147.72

	days := buildDays(temps, humid, speeds, angles, codes, sunriseByDate, sunsetByDate, cloudCover, kpHourly, geomagneticLatitude(testLat, testLon), precip)
	if len(days) != 3 {
		t.Fatalf("expected 3 days, got %d", len(days))
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

	if day.Sunrise == nil || !day.Sunrise.Equal(wantSunrise) {
		t.Errorf("Sunrise = %v, want %v", day.Sunrise, wantSunrise)
	}
	if day.Sunset == nil || !day.Sunset.Equal(wantSunset) {
		t.Errorf("Sunset = %v, want %v", day.Sunset, wantSunset)
	}

	if day.Aurora == nil {
		t.Fatal("expected Aurora to be populated when cloudCover/kpHourly are provided")
	}
	if day.Aurora.MaxProbabilityPct <= 0 {
		t.Errorf("Aurora.MaxProbabilityPct = %.2f, want > 0 (high Kp, clear skies, dark hours present)", day.Aurora.MaxProbabilityPct)
	}
	if len(day.Aurora.Hourly) != len(day.Temperature.Hourly) {
		t.Errorf("Aurora.Hourly length (%d) != Hourly length (%d)", len(day.Aurora.Hourly), len(day.Temperature.Hourly))
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

func TestBuildDaysWithNilCodesDegradesGracefully(t *testing.T) {
	temps := flatSlice(forecastHours, 10.0)
	humid := flatSlice(forecastHours, 80.0)
	speeds := flatSlice(forecastHours, 5.0)
	angles := flatSlice(forecastHours, 180.0)
	precip := precipResult{amounts: flatSlice(forecastHours, 0), probabilities: flatSlice(forecastHours, 0)}

	days := buildDays(temps, humid, speeds, angles, nil, nil, nil, nil, nil, 0, precip)

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
