package serverless

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rehabaam/mycast/config"
	"github.com/rehabaam/mycast/dynamo"
	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/store"
)

var (
	ctx = context.Background()
	// 10:05 UTC: the 10:00 hour is in progress.
	runNow = time.Date(2026, time.June, 15, 10, 5, 0, 0, time.UTC)
)

// memRepo is the table, as a struct.
type memRepo struct {
	state    dynamo.State
	current  *netatmo.Current
	forecast *forecast.Forecast

	saves   map[string]int
	failOn  string
	loadErr error
}

func newMemRepo() *memRepo { return &memRepo{saves: map[string]int{}} }

func (m *memRepo) LoadState(context.Context) (dynamo.State, error) {
	if m.loadErr != nil {
		return dynamo.State{}, m.loadErr
	}
	cp := m.state
	cp.Observations = append([]netatmo.Observation(nil), m.state.Observations...)
	return cp, nil
}

func (m *memRepo) fail(op string) error {
	if m.failOn == op {
		return errors.New("simulated failure of " + op)
	}
	return nil
}

func (m *memRepo) SaveObservations(_ context.Context, obs []netatmo.Observation) error {
	if err := m.fail("observations"); err != nil {
		return err
	}
	m.saves["observations"] += len(obs)
	idx := map[int64]int{}
	for i, o := range m.state.Observations {
		idx[o.Timestamp] = i
	}
	for _, o := range obs {
		if i, ok := idx[o.Timestamp]; ok {
			m.state.Observations[i] = o
		} else {
			m.state.Observations = append(m.state.Observations, o)
		}
	}
	return nil
}

func (m *memRepo) SaveMeta(_ context.Context, last int64, updated time.Time) error {
	if err := m.fail("meta"); err != nil {
		return err
	}
	m.saves["meta"]++
	m.state.LastMeasured, m.state.UpdatedAt = last, updated
	return nil
}

func (m *memRepo) SaveCurrent(_ context.Context, cur *netatmo.Current) error {
	if err := m.fail("current"); err != nil {
		return err
	}
	m.saves["current"]++
	m.current = cur
	return nil
}

func (m *memRepo) SaveForecast(_ context.Context, fc *forecast.Forecast) error {
	if err := m.fail("forecast"); err != nil {
		return err
	}
	m.saves["forecast"]++
	m.forecast = fc
	return nil
}

type fakeSource struct {
	cur     *netatmo.Current
	curErr  error
	hist    []netatmo.Observation
	histErr error
	calls   struct{ current, history int }
}

func (f *fakeSource) GetCurrent() (*netatmo.Current, error) {
	f.calls.current++
	return f.cur, f.curErr
}

func (f *fakeSource) GetHistory(_ netatmo.Station, from, to time.Time) ([]netatmo.Observation, error) {
	f.calls.history++
	if f.histErr != nil {
		return nil, f.histErr
	}
	var out []netatmo.Observation
	for _, o := range f.hist {
		if ts := time.Unix(o.Timestamp, 0); !ts.Before(from) && !ts.After(to) {
			out = append(out, o)
		}
	}
	return out, nil
}

// completedHistory is hourly aggregates for the 100 hours before 10:00,
// stamped mid-interval as Netatmo's are.
func completedHistory() []netatmo.Observation {
	var out []netatmo.Observation
	for h := 100; h >= 1; h-- {
		at := runNow.Truncate(time.Hour).Add(-time.Duration(h) * time.Hour).Add(30 * time.Minute)
		out = append(out, netatmo.Observation{Timestamp: at.Unix(), Temperature: 12, Humidity: 70, WindSpeed: 5, WindAngle: 180, Rain: 0})
	}
	return out
}

func liveAt(measured time.Time) *netatmo.Current {
	return &netatmo.Current{
		Station:          netatmo.Station{ID: "70:ee:50:00:00:01", OutdoorModuleID: "02:00:00:00:00:01"},
		FetchedAt:        runNow,
		Timestamp:        measured.Unix(),
		OutdoorTimestamp: measured.Unix(),
		OutdoorAvailable: true,
		WindAvailable:    true,
		OutdoorTemp:      14, OutdoorHumidity: 70, WindSpeed: 6, WindAngle: 200,
	}
}

func newIngestor(repo Repository, src *fakeSource, now time.Time) *Ingestor {
	return &Ingestor{
		Repo:   repo,
		Source: src,
		Config: &config.Config{HistoryDays: 7, FetchIntervalMin: 30},
		Now:    func() time.Time { return now },
		// No network: the station-only model, from whatever history is stored.
		NewEngine: func(_ *config.Config, st netatmo.Station, ts *store.TimeSeries) *forecast.Engine {
			return forecast.NewEngine(ts, forecast.Config{StationID: st.ID, StaleAfter: time.Hour})
		},
	}
}

func TestFirstRunOnAnEmptyStoreBackfillsAndSavesEverything(t *testing.T) {
	repo := newMemRepo()
	src := &fakeSource{cur: liveAt(runNow.Add(-2 * time.Minute)), hist: completedHistory()}

	res, err := newIngestor(repo, src, runNow).Run(ctx)

	if err != nil {
		t.Fatal(err)
	}
	if res.CaughtUp != 100 {
		t.Errorf("CaughtUp = %d, want the 100 hours of history", res.CaughtUp)
	}
	if !res.CurrentSaved || !res.MetaSaved || !res.ForecastSaved {
		t.Errorf("result = %+v, want current, meta and forecast saved", res)
	}
	if res.ObservationsSaved < 100 {
		t.Errorf("ObservationsSaved = %d, want the backfilled hours plus the live one", res.ObservationsSaved)
	}
	if repo.forecast == nil || len(repo.forecast.Days) == 0 {
		t.Fatalf("no forecast saved: %+v", repo.forecast)
	}
	if repo.state.UpdatedAt.IsZero() || repo.state.LastMeasured != runNow.Add(-2*time.Minute).Unix() {
		t.Errorf("staleness state = %v / %d, want set from the live reading", repo.state.UpdatedAt, repo.state.LastMeasured)
	}
	if repo.current == nil || repo.current.OutdoorTemp != 14 {
		t.Errorf("current reading not saved: %+v", repo.current)
	}
}

func TestASecondRunWritesOnlyWhatChanged(t *testing.T) {
	repo := newMemRepo()
	src := &fakeSource{cur: liveAt(runNow.Add(-2 * time.Minute)), hist: completedHistory()}
	if _, err := newIngestor(repo, src, runNow).Run(ctx); err != nil {
		t.Fatal(err)
	}
	storedBefore := len(repo.state.Observations)
	repo.saves = map[string]int{}

	// Half an hour on, a new live reading arrives; history is unchanged.
	later := runNow.Add(30 * time.Minute)
	src.cur = liveAt(later.Add(-2 * time.Minute))
	src.cur.OutdoorTemp = 15 // it has warmed up since the 10:03 reading
	src.calls.history = 0
	res, err := newIngestor(repo, src, later).Run(ctx)

	if err != nil {
		t.Fatal(err)
	}
	if res.CaughtUp != 0 {
		t.Errorf("CaughtUp = %d: a store that is current needs no catch-up", res.CaughtUp)
	}
	// 100+ hours are stored; only the live 10:00 hour differs. The completed
	// hours reconcile to the values already stored, so they are not rewritten.
	if res.ObservationsSaved != 1 {
		t.Errorf("ObservationsSaved = %d, want exactly 1 (the live hour)", res.ObservationsSaved)
	}
	if !res.MetaSaved {
		t.Error("a newer measurement should have advanced the staleness state")
	}
	if got := len(repo.state.Observations); got > storedBefore+1 {
		t.Errorf("store grew from %d to %d items", storedBefore, got)
	}
	if !res.ForecastSaved || repo.saves["forecast"] != 1 {
		t.Errorf("forecast saves = %d, want 1", repo.saves["forecast"])
	}
}

// A module that has gone quiet re-delivers the same measurement on every run.
// Its time must not be taken as the station reporting, or staleness is masked.
func TestAQuietModuleDoesNotRefreshTheStalenessState(t *testing.T) {
	repo := newMemRepo()
	measured := runNow.Add(-2 * time.Minute)
	src := &fakeSource{cur: liveAt(measured), hist: completedHistory()}
	if _, err := newIngestor(repo, src, runNow).Run(ctx); err != nil {
		t.Fatal(err)
	}
	updated := repo.state.UpdatedAt
	repo.saves = map[string]int{}

	// The base ticks on; the outdoor module's measurement is the same one.
	time.Sleep(10 * time.Millisecond)
	again := liveAt(measured)
	again.Timestamp += 30 * 60
	src.cur = again
	res, err := newIngestor(repo, src, runNow.Add(30*time.Minute)).Run(ctx)

	if err != nil {
		t.Fatal(err)
	}
	if res.MetaSaved || repo.saves["meta"] != 0 {
		t.Error("the staleness state was rewritten for a measurement that was not new")
	}
	if !repo.state.UpdatedAt.Equal(updated) {
		t.Errorf("UpdatedAt moved from %v to %v", updated, repo.state.UpdatedAt)
	}
}

func TestAnUnavailableOutdoorModuleSavesTheReadingButNothingElse(t *testing.T) {
	repo := newMemRepo()
	cur := liveAt(runNow.Add(-2 * time.Minute))
	cur.OutdoorAvailable = false
	src := &fakeSource{cur: cur, hist: completedHistory()}

	res, err := newIngestor(repo, src, runNow).Run(ctx)

	if err != nil {
		t.Fatal(err)
	}
	if !res.CurrentSaved {
		t.Error("the reading (indoor, pressure, module health) was not saved")
	}
	if res.MetaSaved || res.ForecastSaved {
		t.Errorf("result = %+v: a run with no outdoor data must not claim the station reported or recompute", res)
	}
	if !repo.state.UpdatedAt.IsZero() {
		t.Error("history alone marked the station as reporting")
	}
}

func TestAFetchFailureFailsTheRunAndSavesNothing(t *testing.T) {
	repo := newMemRepo()
	src := &fakeSource{curErr: errors.New("netatmo down")}

	_, err := newIngestor(repo, src, runNow).Run(ctx)

	if err == nil {
		t.Fatal("expected an error so the invocation is recorded as failed")
	}
	if len(repo.saves) != 0 {
		t.Errorf("saves = %v, want none", repo.saves)
	}
}

func TestAHistoryFailureDoesNotLoseTheLiveReading(t *testing.T) {
	repo := newMemRepo()
	src := &fakeSource{cur: liveAt(runNow.Add(-2 * time.Minute)), histErr: errors.New("netatmo history down")}

	res, err := newIngestor(repo, src, runNow).Run(ctx)

	if err != nil {
		t.Fatalf("a history failure must not fail the run: %v", err)
	}
	if !res.CurrentSaved || !res.MetaSaved || res.ObservationsSaved != 1 {
		t.Errorf("result = %+v, want the live reading saved", res)
	}
}

func TestEachStorageFailureIsReported(t *testing.T) {
	for _, op := range []string{"current", "observations", "meta", "forecast"} {
		repo := newMemRepo()
		repo.failOn = op
		src := &fakeSource{cur: liveAt(runNow.Add(-2 * time.Minute)), hist: completedHistory()}

		if _, err := newIngestor(repo, src, runNow).Run(ctx); err == nil {
			t.Errorf("a failure saving %s was swallowed", op)
		}
	}

	repo := newMemRepo()
	repo.loadErr = errors.New("dynamodb unavailable")
	if _, err := newIngestor(repo, &fakeSource{}, runNow).Run(ctx); err == nil {
		t.Error("a failure loading state was swallowed")
	}
}

// If a run dies part-way, the forecast must never be newer than the data it
// was computed from.
func TestTheForecastIsSavedLast(t *testing.T) {
	repo := newMemRepo()
	repo.failOn = "observations"
	src := &fakeSource{cur: liveAt(runNow.Add(-2 * time.Minute)), hist: completedHistory()}

	_, _ = newIngestor(repo, src, runNow).Run(ctx)

	if repo.forecast != nil {
		t.Error("a forecast was saved although the observations it was built from were not")
	}
}

func TestChangedObservations(t *testing.T) {
	o := func(ts int64, temp float64) netatmo.Observation {
		return netatmo.Observation{Timestamp: ts, Temperature: temp}
	}
	before := []netatmo.Observation{o(1, 1), o(2, 2), o(3, 3)}
	after := []netatmo.Observation{o(1, 1), o(2, 22), o(3, 3), o(4, 4)}

	got := changedObservations(before, after)

	if len(got) != 2 || got[0].Timestamp != 2 || got[1].Timestamp != 4 {
		t.Errorf("changed = %+v, want hours 2 (changed) and 4 (new)", got)
	}
	if len(changedObservations(after, after)) != 0 {
		t.Error("identical series reported as changed")
	}
}
