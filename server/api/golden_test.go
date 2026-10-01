package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// Golden /current responses, shared with the Swift app's tests (see
// forecast/golden_test.go for the rationale).
//
//	go test ./api -run Golden -update
var update = flag.Bool("update", false, "rewrite the golden fixtures shared with the Swift app's tests")

const fixtureDir = "../../app/MyCastTests/Fixtures"

func checkGolden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join(fixtureDir, name)
	if *update {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\nregenerate with: go test ./api -run Golden -update", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s no longer matches the server's output; regenerate with: go test ./api -run Golden -update", name)
	}
}

func TestGoldenCurrent(t *testing.T) {
	checkGolden(t, "current.json", newCurrentResponse(sampleCurrent()))
}

func TestGoldenCurrentOutdoorOffline(t *testing.T) {
	cur := sampleCurrent()
	cur.OutdoorAvailable = false
	cur.OutdoorTemp, cur.OutdoorHumidity = 0, 0
	cur.Modules[0].Reachable = false
	checkGolden(t, "current_outdoor_offline.json", newCurrentResponse(cur))
}
