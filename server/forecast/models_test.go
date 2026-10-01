package forecast

import "testing"

func TestDegreeToCardinal(t *testing.T) {
	cases := map[float64]string{
		0:      "N",
		11.24:  "N",
		11.25:  "NNE", // exact sector boundary belongs to the next sector
		22.5:   "NNE",
		90:     "E",
		180:    "S",
		270:    "W",
		348.74: "NNW",
		348.75: "N",
		359.99: "N",
		360:    "N",
		-90:    "W",
		450:    "E",
	}
	for deg, want := range cases {
		if got := degreeToCardinal(deg); got != want {
			t.Errorf("degreeToCardinal(%v) = %q, want %q", deg, got, want)
		}
	}
}
