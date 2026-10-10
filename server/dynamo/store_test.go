package dynamo

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"golang.org/x/oauth2"

	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
)

// fakeTable is an in-memory DynamoDB table, enough for this package: items
// keyed by (pk, sk), ordered queries on a sort-key prefix, and pagination.
type fakeTable struct {
	items    map[string]map[string]types.AttributeValue
	pageSize int
	err      error

	puts, gets, queries int
}

func newFakeTable() *fakeTable {
	return &fakeTable{items: map[string]map[string]types.AttributeValue{}, pageSize: 1000}
}

func str(v types.AttributeValue) string { return v.(*types.AttributeValueMemberS).Value }

func ik(item map[string]types.AttributeValue) string { return str(item["pk"]) + "|" + str(item["sk"]) }

func (f *fakeTable) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.puts++
	f.items[ik(in.Item)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeTable) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.gets++
	return &dynamodb.GetItemOutput{Item: f.items[ik(in.Key)]}, nil
}

func (f *fakeTable) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.queries++
	pk, prefix := str(in.ExpressionAttributeValues[":pk"]), str(in.ExpressionAttributeValues[":p"])

	var sks []string
	for _, it := range f.items {
		if str(it["pk"]) == pk && strings.HasPrefix(str(it["sk"]), prefix) {
			sks = append(sks, str(it["sk"]))
		}
	}
	sort.Strings(sks)
	if in.ExclusiveStartKey != nil {
		after := str(in.ExclusiveStartKey["sk"])
		i := sort.SearchStrings(sks, after)
		if i < len(sks) && sks[i] == after {
			i++
		}
		sks = sks[i:]
	}

	out := &dynamodb.QueryOutput{}
	for i, sk := range sks {
		if i == f.pageSize {
			out.LastEvaluatedKey = map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: pk},
				"sk": &types.AttributeValueMemberS{Value: sks[i-1]},
			}
			break
		}
		out.Items = append(out.Items, f.items[pk+"|"+sk])
	}
	return out, nil
}

var ctx = context.Background()

func obs(hour int64, temp float64, has netatmo.Fields) netatmo.Observation {
	return netatmo.Observation{Timestamp: hour * 3600, Temperature: temp, Humidity: 50, WindSpeed: 7, WindAngle: 90, GustSpeed: 12, GustAngle: 100, Rain: 0.3, Has: has}
}

func TestObservationsRoundTripInTimeOrder(t *testing.T) {
	st := New(newFakeTable(), "t")
	in := []netatmo.Observation{obs(500, 5, netatmo.FieldsAll), obs(100, 1, netatmo.FieldOutdoor), obs(300, 3, netatmo.FieldOutdoor|netatmo.FieldWind)}

	if err := st.SaveObservations(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadObservations(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d observations, want 3", len(got))
	}
	for i, wantTS := range []int64{100 * 3600, 300 * 3600, 500 * 3600} {
		if got[i].Timestamp != wantTS {
			t.Errorf("got[%d].Timestamp = %d, want %d (ascending)", i, got[i].Timestamp, wantTS)
		}
	}
	if got[0].Has != netatmo.FieldOutdoor || got[1].Has != netatmo.FieldOutdoor|netatmo.FieldWind {
		t.Errorf("field groups lost: %b / %b", got[0].Has, got[1].Has)
	}
	want := obs(100, 1, netatmo.FieldOutdoor)
	if got[0] != want {
		t.Errorf("got[0] = %+v, want %+v", got[0], want)
	}
}

func TestZeroHasIsStoredAsEveryGroup(t *testing.T) {
	st := New(newFakeTable(), "t")
	if err := st.SaveObservations(ctx, []netatmo.Observation{obs(100, 1, 0)}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.LoadObservations(ctx)
	if len(got) != 1 || got[0].Has != netatmo.FieldsAll {
		t.Errorf("Has = %b, want all groups stored explicitly", got[0].Has)
	}
}

func TestSavingAnHourTwiceKeepsOneItem(t *testing.T) {
	tbl := newFakeTable()
	st := New(tbl, "t")
	_ = st.SaveObservations(ctx, []netatmo.Observation{obs(100, 1, 0)})
	_ = st.SaveObservations(ctx, []netatmo.Observation{obs(100, 9, 0)})

	got, _ := st.LoadObservations(ctx)
	if len(got) != 1 || got[0].Temperature != 9 {
		t.Errorf("got %+v, want one item with the later value", got)
	}
}

func TestObservationsExpireAfterTheConfiguredTTL(t *testing.T) {
	tbl := newFakeTable()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	st := New(tbl, "t", WithObservationTTL(8*24*time.Hour))
	st.now = func() time.Time { return now }

	_ = st.SaveObservations(ctx, []netatmo.Observation{obs(100, 1, 0)})

	item := tbl.items["mycast|"+obsSK(100*3600)]
	var ttl int64
	if n, ok := item["ttl"].(*types.AttributeValueMemberN); ok {
		for _, c := range n.Value {
			ttl = ttl*10 + int64(c-'0')
		}
	}
	if want := now.Add(8 * 24 * time.Hour).Unix(); ttl != want {
		t.Errorf("ttl = %d, want %d", ttl, want)
	}
}

func TestLoadObservationsFollowsPagination(t *testing.T) {
	tbl := newFakeTable()
	tbl.pageSize = 3
	st := New(tbl, "t")
	var in []netatmo.Observation
	for h := int64(1); h <= 10; h++ {
		in = append(in, obs(h, float64(h), 0))
	}
	_ = st.SaveObservations(ctx, in)

	got, err := st.LoadObservations(ctx)

	if err != nil || len(got) != 10 {
		t.Fatalf("got %d observations (err %v), want all 10", len(got), err)
	}
	if tbl.queries < 4 {
		t.Errorf("made %d queries, want at least 4 pages of 3", tbl.queries)
	}
	for i, o := range got {
		if o.Timestamp != int64(i+1)*3600 {
			t.Fatalf("out of order at %d: %d", i, o.Timestamp)
		}
	}
}

func TestObservationQueryDoesNotReturnOtherItems(t *testing.T) {
	st := New(newFakeTable(), "t")
	_ = st.SaveObservations(ctx, []netatmo.Observation{obs(1, 1, 0)})
	_ = st.SaveMeta(ctx, 5, time.Unix(1000, 0))
	_ = st.SaveCurrent(ctx, &netatmo.Current{OutdoorTemp: 3})
	_ = st.SaveForecast(ctx, &forecast.Forecast{Model: "m"})
	_ = st.TokenStore().Save(&oauth2.Token{AccessToken: "a"})

	got, err := st.LoadObservations(ctx)

	if err != nil || len(got) != 1 {
		t.Errorf("got %d items (err %v), want only the observation", len(got), err)
	}
}

func TestMetaRoundTripAndMissing(t *testing.T) {
	st := New(newFakeTable(), "t")

	last, updated, err := st.LoadMeta(ctx)
	if err != nil || last != 0 || !updated.IsZero() {
		t.Fatalf("empty LoadMeta = (%d, %v, %v), want zero values and no error", last, updated, err)
	}

	if err := st.SaveMeta(ctx, 1781000100, time.Unix(1781000200, 0)); err != nil {
		t.Fatal(err)
	}
	last, updated, err = st.LoadMeta(ctx)
	if err != nil || last != 1781000100 || updated.Unix() != 1781000200 {
		t.Errorf("LoadMeta = (%d, %v, %v)", last, updated, err)
	}
}

func TestLoadState(t *testing.T) {
	st := New(newFakeTable(), "t")
	_ = st.SaveObservations(ctx, []netatmo.Observation{obs(2, 2, 0), obs(1, 1, 0)})
	_ = st.SaveMeta(ctx, 7200, time.Unix(9000, 0))

	got, err := st.LoadState(ctx)

	if err != nil || len(got.Observations) != 2 || got.LastMeasured != 7200 || got.UpdatedAt.Unix() != 9000 {
		t.Errorf("LoadState = %+v, %v", got, err)
	}
}

func TestCurrentRoundTrip(t *testing.T) {
	st := New(newFakeTable(), "t")
	in := &netatmo.Current{
		FetchedAt: time.Date(2026, 6, 15, 12, 0, 5, 0, time.UTC), Timestamp: 1781000000, OutdoorTimestamp: 1780999900,
		OutdoorAvailable: true, WindAvailable: true, OutdoorTemp: 16.6, SumRain1h: 0.2,
		Station: netatmo.Station{ID: "70:ee:50:00:00:01", Lat: 60.17, Lon: 24.94, HasLocation: true, Timezone: "Europe/Helsinki"},
		Modules: []netatmo.ModuleStatus{{Type: "NAModule1", Name: "Outdoor", BatteryPercent: 80, Reachable: true}},
	}
	if err := st.SaveCurrent(ctx, in); err != nil {
		t.Fatal(err)
	}

	got, err := st.LoadCurrent(ctx)

	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(in)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Errorf("round trip changed the reading:\n in: %s\nout: %s", a, b)
	}
}

// The real forecast, from the golden fixture the Swift tests also decode: it
// must fit an item comfortably and survive storage unchanged.
func TestForecastRoundTripAndSize(t *testing.T) {
	raw, err := os.ReadFile("../../app/MyCastTests/Fixtures/forecast_full.json")
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	var fc forecast.Forecast
	if err := json.Unmarshal(raw, &fc); err != nil {
		t.Fatal(err)
	}
	tbl := newFakeTable()
	st := New(tbl, "t")

	if err := st.SaveForecast(ctx, &fc); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadForecast(ctx)
	if err != nil {
		t.Fatal(err)
	}

	again, _ := json.Marshal(got)
	orig, _ := json.Marshal(&fc)
	if string(again) != string(orig) {
		t.Error("the forecast changed on its way through storage")
	}

	stored := len(tbl.items["mycast|FORECAST"]["data"].(*types.AttributeValueMemberB).Value)
	const itemLimit = 400 * 1024
	if stored >= itemLimit/4 {
		t.Errorf("forecast stored as %d bytes: too close to DynamoDB's %d-byte item limit", stored, itemLimit)
	}
	if stored >= len(orig)/2 {
		t.Errorf("stored %d bytes for %d of JSON: compression is not doing its job", stored, len(orig))
	}
	t.Logf("forecast: %d bytes of JSON stored as %d bytes", len(orig), stored)
}

func TestMissingItemsAreNotFound(t *testing.T) {
	st := New(newFakeTable(), "t")

	if _, err := st.LoadCurrent(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("LoadCurrent err = %v, want ErrNotFound", err)
	}
	if _, err := st.LoadForecast(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("LoadForecast err = %v, want ErrNotFound", err)
	}
	if !errors.Is(ErrNotFound, fs.ErrNotExist) {
		t.Error("ErrNotFound must satisfy fs.ErrNotExist so 'no token yet' is recognised")
	}
}

func TestTokenStoreRoundTripAndMissing(t *testing.T) {
	ts := New(newFakeTable(), "t").TokenStore()

	if _, err := ts.Load(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load on empty = %v, want fs.ErrNotExist", err)
	}

	exp := time.Date(2026, 6, 15, 15, 0, 0, 0, time.UTC)
	if err := ts.Save(&oauth2.Token{AccessToken: "at", RefreshToken: "rt", TokenType: "bearer", Expiry: exp}); err != nil {
		t.Fatal(err)
	}
	got, err := ts.Load()
	if err != nil || got.AccessToken != "at" || got.RefreshToken != "rt" || !got.Expiry.Equal(exp) {
		t.Errorf("Load = (%+v, %v)", got, err)
	}
}

func TestStorageErrorsAreReturnedNotSwallowed(t *testing.T) {
	tbl := newFakeTable()
	tbl.err = errors.New("ProvisionedThroughputExceededException")
	st := New(tbl, "t")

	if err := st.SaveObservations(ctx, []netatmo.Observation{obs(1, 1, 0)}); err == nil {
		t.Error("SaveObservations swallowed a storage error")
	}
	if _, err := st.LoadState(ctx); err == nil {
		t.Error("LoadState swallowed a storage error")
	}
	if _, err := st.TokenStore().Load(); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("token Load err = %v: an outage must not look like 'no token'", err)
	}
}

// --- Reader ---

func readerAt(st *Store, now time.Time, staleAfter time.Duration) *Reader {
	r := st.Reader(staleAfter)
	r.now = func() time.Time { return now }
	return r
}

func TestReaderFlagsStalenessFromTheStationsReporting(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	day := []forecast.DayForecast{{Date: "2026-06-15"}}
	cases := []struct {
		name      string
		days      []forecast.DayForecast
		updatedAt time.Time
		want      bool
	}{
		{"fresh", day, now.Add(-10 * time.Minute), false},
		{"exactly at the threshold", day, now.Add(-time.Hour), false},
		{"station silent", day, now.Add(-3 * time.Hour), true},
		{"never reported", day, time.Time{}, true},
		{"no days", nil, now, true},
	}
	for _, c := range cases {
		st := New(newFakeTable(), "t")
		_ = st.SaveForecast(ctx, &forecast.Forecast{Model: "m", Days: c.days})
		_ = st.SaveMeta(ctx, 1, c.updatedAt)

		fc, err := readerAt(st, now, time.Hour).Forecast(ctx)

		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if fc.Stale != c.want {
			t.Errorf("%s: Stale = %v, want %v", c.name, fc.Stale, c.want)
		}
		if fc.Days == nil {
			t.Errorf("%s: Days is nil, want [] so it serialises as an array", c.name)
		}
	}
}

func TestReaderWithNothingStoredIsAnEmptyStaleForecast(t *testing.T) {
	fc, err := readerAt(New(newFakeTable(), "t"), time.Now(), time.Hour).Forecast(ctx)

	if err != nil {
		t.Fatal(err)
	}
	if !fc.Stale || fc.Days == nil || len(fc.Days) != 0 {
		t.Errorf("forecast = %+v, want stale with days []", fc)
	}
}

func TestReaderSurfacesStorageFailuresOnTheForecast(t *testing.T) {
	tbl := newFakeTable()
	st := New(tbl, "t")
	_ = st.SaveForecast(ctx, &forecast.Forecast{Days: []forecast.DayForecast{{}}})
	tbl.err = errors.New("AccessDeniedException")

	if _, err := readerAt(st, time.Now(), time.Hour).Forecast(ctx); err == nil {
		t.Error("a storage failure was reported as an empty forecast")
	}
}

func TestReaderLatestAndAll(t *testing.T) {
	tbl := newFakeTable()
	st := New(tbl, "t")
	r := readerAt(st, time.Now(), time.Hour)

	if cur, ok := r.Latest(); ok || cur != nil {
		t.Errorf("Latest on empty = (%v, %v)", cur, ok)
	}
	if len(r.All()) != 0 {
		t.Error("All on empty is not empty")
	}

	_ = st.SaveCurrent(ctx, &netatmo.Current{OutdoorTemp: 12})
	_ = st.SaveObservations(ctx, []netatmo.Observation{obs(1, 1, 0), obs(2, 2, 0)})
	if cur, ok := r.Latest(); !ok || cur.OutdoorTemp != 12 {
		t.Errorf("Latest = (%+v, %v)", cur, ok)
	}
	if len(r.All()) != 2 {
		t.Errorf("All returned %d, want 2", len(r.All()))
	}

	tbl.err = errors.New("boom")
	if cur, ok := r.Latest(); ok || cur != nil {
		t.Error("Latest reported a reading during a storage failure")
	}
	if r.All() != nil {
		t.Error("All returned data during a storage failure")
	}
}
