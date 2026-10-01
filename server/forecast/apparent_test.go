package forecast

import "testing"

func TestApparentTemperatureWindMakesColdFeelColder(t *testing.T) {
	calm := ApparentTemperatureC(0, 70, 0)
	windy := ApparentTemperatureC(0, 70, 30)
	if windy >= calm {
		t.Errorf("windy apparent temp (%.2f) should be lower than calm (%.2f) at freezing", windy, calm)
	}
}

func TestApparentTemperatureHumidityMakesHotFeelHotter(t *testing.T) {
	dry := ApparentTemperatureC(30, 30, 10)
	humid := ApparentTemperatureC(30, 90, 10)
	if humid <= dry {
		t.Errorf("humid apparent temp (%.2f) should be higher than dry (%.2f) at 30°C", humid, dry)
	}
}
