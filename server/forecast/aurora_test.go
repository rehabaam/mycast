package forecast

import (
	"testing"

	"github.com/rehabaam/mycast/noaa"
)

func TestGeomagneticLatitudeFairbanks(t *testing.T) {
	// Fairbanks, Alaska is a well-documented public aurora reference point
	// (chosen specifically because it's publicly known, not tied to any
	// particular station's real location): sitting almost directly under
	// the auroral oval, it's known to need only Kp ~1-2 for visibility.
	got := geomagneticLatitude(64.84, -147.72)
	if got < 63 || got > 68 {
		t.Errorf("geomagneticLatitude(Fairbanks) = %.1f, want ~65-66 (known reference range 63-68)", got)
	}
}

func TestGeomagneticLatitudeEquator(t *testing.T) {
	// Geomagnetic latitude at the geographic equator should still be
	// roughly equatorial (nowhere near the poles), regardless of longitude.
	got := geomagneticLatitude(0, 0)
	if got < -30 || got > 30 {
		t.Errorf("geomagneticLatitude(0,0) = %.1f, want within ~30° of the equator", got)
	}
}

func TestRequiredKpMatchesTableAtExactBoundaries(t *testing.T) {
	cases := map[float64]float64{
		66.5: 0,
		56.3: 5,
		48.1: 9,
	}
	for lat, wantKp := range cases {
		if got := requiredKp(lat); got != wantKp {
			t.Errorf("requiredKp(%.1f) = %.2f, want %.2f", lat, got, wantKp)
		}
	}
}

func TestRequiredKpHigherLatitudeNeedsLessKp(t *testing.T) {
	// A higher geomagnetic latitude (closer to the auroral oval) should
	// need less geomagnetic activity to see aurora than a lower one.
	lower := requiredKp(57.7)
	higher := requiredKp(64.0)
	if higher >= lower {
		t.Errorf("requiredKp(higher)=%.2f should be less than requiredKp(lower)=%.2f", higher, lower)
	}
}

func TestRequiredKpClampsBeyondTableRange(t *testing.T) {
	if got := requiredKp(80); got != 0 {
		t.Errorf("requiredKp(80) = %.2f, want 0 (clamped, oval always reaches this far)", got)
	}
	if got := requiredKp(30); got != 9 {
		t.Errorf("requiredKp(30) = %.2f, want 9 (clamped, effectively never visible)", got)
	}
}

func TestAuroraProbabilityZeroInDaylight(t *testing.T) {
	if got := auroraProbabilityPct(9, 4, 0, false); got != 0 {
		t.Errorf("auroraProbabilityPct(daylight) = %.2f, want 0 regardless of Kp", got)
	}
}

func TestAuroraProbabilityZeroBelowThreshold(t *testing.T) {
	if got := auroraProbabilityPct(2, 4.3, 0, true); got != 0 {
		t.Errorf("auroraProbabilityPct(kp<required) = %.2f, want 0", got)
	}
}

func TestAuroraProbabilityScalesWithMarginAboveThreshold(t *testing.T) {
	// 0% at the threshold, rising linearly to 100% three Kp steps above it.
	cases := []struct{ kp, want float64 }{
		{4.3, 0},
		{4.8, 100.0 / 6},
		{5.8, 50},
		{6.8, 100.0 * 5 / 6},
		{7.3, 100},
		{9.0, 100}, // saturates
	}
	for _, c := range cases {
		got := auroraProbabilityPct(c.kp, 4.3, 0, true)
		if d := got - c.want; d > 1e-9 || d < -1e-9 {
			t.Errorf("auroraProbabilityPct(kp=%.1f) = %.4f, want %.4f", c.kp, got, c.want)
		}
	}
}

func TestAuroraProbabilityDeratesForCloudCover(t *testing.T) {
	clear := auroraProbabilityPct(7, 4, 0, true)
	overcast := auroraProbabilityPct(7, 4, 100, true)
	half := auroraProbabilityPct(7, 4, 50, true)
	if overcast != 0 {
		t.Errorf("auroraProbabilityPct(100%% cloud) = %.2f, want 0", overcast)
	}
	if clear != 100 {
		t.Errorf("auroraProbabilityPct(clear) = %.2f, want 100", clear)
	}
	if half != 50 {
		t.Errorf("auroraProbabilityPct(50%% cloud) = %.2f, want 50 (derated linearly)", half)
	}
}

func TestKpForHourFloorLookup(t *testing.T) {
	points := []noaa.KpPoint{
		{Time: 1000, Kp: 2.0},
		{Time: 4600, Kp: 5.0},
		{Time: 8200, Kp: 3.0},
	}
	cases := map[int64]float64{
		1000: 2.0, // exact match
		2000: 2.0, // between first and second -> floor to first
		4600: 5.0, // exact match on second
		9000: 3.0, // past all points -> last one
		0:    2.0, // before all points -> first one
	}
	for ts, want := range cases {
		if got := kpForHour(points, ts); got != want {
			t.Errorf("kpForHour(%d) = %.1f, want %.1f", ts, got, want)
		}
	}
}
