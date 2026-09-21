package forecast

import "testing"

func TestConditionSummaryKnownCodes(t *testing.T) {
	cases := map[int]string{
		0:  "Clear sky",
		3:  "Overcast",
		61: "Slight rain",
		95: "Thunderstorm",
	}
	for code, want := range cases {
		if got := conditionSummary(code); got != want {
			t.Errorf("conditionSummary(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestConditionSummaryUnknownCode(t *testing.T) {
	if got := conditionSummary(-1); got != "Unknown" {
		t.Errorf("conditionSummary(-1) = %q, want %q", got, "Unknown")
	}
	if got := conditionSummary(12345); got != "Unknown" {
		t.Errorf("conditionSummary(12345) = %q, want %q", got, "Unknown")
	}
}
