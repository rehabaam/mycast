// Package dynamo persists mycast's state in a single DynamoDB table, so that
// short-lived processes (the Lambda functions) can pick up where the last one
// stopped.
//
// Every item shares one partition key and is told apart by its sort key:
//
//	OBS#<unix hour, 10 digits>   one observation per clock hour (expires)
//	META                         when the station last reported (staleness)
//	CURRENT                      the latest live reading served by /current
//	FORECAST                     the forecast computed by the last ingest
//	TOKEN                        the Netatmo OAuth2 token
//
// The zero-padded hour makes sort-key order time order, so loading the
// observations is one ordered Query.
package dynamo

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"golang.org/x/oauth2"

	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
)

// ErrNotFound is returned when an item does not exist. It satisfies
// errors.Is(err, fs.ErrNotExist), which is how the OAuth code recognises "no
// token stored yet".
var ErrNotFound = fmt.Errorf("item not found: %w", fs.ErrNotExist)

const (
	defaultPartition = "mycast"
	obsPrefix        = "OBS#"
	skMeta           = "META"
	skCurrent        = "CURRENT"
	skForecast       = "FORECAST"
	skToken          = "TOKEN"

	// maxBlobBytes bounds what is inflated from a stored blob.
	maxBlobBytes = 4 << 20
)

// API is the part of *dynamodb.Client this package uses, so tests can
// substitute an in-memory table.
type API interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// Store reads and writes mycast's state.
type Store struct {
	db     API
	table  string
	pk     string
	obsTTL time.Duration
	now    func() time.Time
}

// Option customises a Store.
type Option func(*Store)

// WithObservationTTL sets how long an observation lives before DynamoDB's TTL
// removes it. It should comfortably exceed the history the service keeps.
func WithObservationTTL(d time.Duration) Option {
	return func(s *Store) { s.obsTTL = d }
}

// New returns a Store over an existing client.
func New(db API, table string, opts ...Option) *Store {
	s := &Store{db: db, table: table, pk: defaultPartition, obsTTL: 31 * 24 * time.Hour, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Open returns a Store using the default AWS credential chain and region.
func Open(ctx context.Context, table string, opts ...Option) (*Store, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return New(dynamodb.NewFromConfig(cfg), table, opts...), nil
}

// --- keys and items ---

func obsSK(unixHour int64) string { return fmt.Sprintf("%s%010d", obsPrefix, unixHour) }

func (s *Store) key(sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: s.pk},
		"sk": &types.AttributeValueMemberS{Value: sk},
	}
}

// obsItem is how an observation is stored. It is separate from
// netatmo.Observation so the stored format doesn't move when that type does.
type obsItem struct {
	PK          string  `dynamodbav:"pk"`
	SK          string  `dynamodbav:"sk"`
	TTL         int64   `dynamodbav:"ttl"`
	Timestamp   int64   `dynamodbav:"ts"`
	Temperature float64 `dynamodbav:"temp"`
	Humidity    float64 `dynamodbav:"hum"`
	WindSpeed   float64 `dynamodbav:"ws"`
	WindAngle   float64 `dynamodbav:"wa"`
	GustSpeed   float64 `dynamodbav:"gs"`
	GustAngle   float64 `dynamodbav:"ga"`
	Rain        float64 `dynamodbav:"rain"`
	Has         uint8   `dynamodbav:"has"`
}

type metaItem struct {
	PK           string `dynamodbav:"pk"`
	SK           string `dynamodbav:"sk"`
	UpdatedAt    int64  `dynamodbav:"updated_at"`
	LastMeasured int64  `dynamodbav:"last_measured"`
}

type blobItem struct {
	PK   string `dynamodbav:"pk"`
	SK   string `dynamodbav:"sk"`
	Data []byte `dynamodbav:"data"`
}

func (s *Store) put(ctx context.Context, item any) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal item: %w", err)
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: av})
	return err
}

func (s *Store) get(ctx context.Context, sk string, out any) error {
	res, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.table),
		Key:            s.key(sk),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return err
	}
	if len(res.Item) == 0 {
		return ErrNotFound
	}
	return attributevalue.UnmarshalMap(res.Item, out)
}

// putBlob stores v as gzip-compressed JSON. The forecast is tens of kilobytes
// of repetitive JSON, and DynamoDB bills writes per kilobyte.
func (s *Store) putBlob(ctx context.Context, sk string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", sk, err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return s.put(ctx, blobItem{PK: s.pk, SK: sk, Data: buf.Bytes()})
}

func (s *Store) getBlob(ctx context.Context, sk string, v any) error {
	var item blobItem
	if err := s.get(ctx, sk, &item); err != nil {
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(item.Data))
	if err != nil {
		return fmt.Errorf("decode %s: %w", sk, err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, maxBlobBytes))
	if err != nil {
		return fmt.Errorf("decode %s: %w", sk, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decode %s: %w", sk, err)
	}
	return nil
}

// --- observations and staleness state ---

// State is what a time series needs to resume: its observations and the
// staleness bookkeeping that goes with them (see store.TimeSeries.Restore).
type State struct {
	Observations []netatmo.Observation
	LastMeasured int64
	UpdatedAt    time.Time
}

// LoadObservations returns every stored observation in ascending time order.
func (s *Store) LoadObservations(ctx context.Context) ([]netatmo.Observation, error) {
	var out []netatmo.Observation
	var start map[string]types.AttributeValue
	for {
		res, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.table),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :p)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: s.pk},
				":p":  &types.AttributeValueMemberS{Value: obsPrefix},
			},
			ConsistentRead:    aws.Bool(true),
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, err
		}
		for _, raw := range res.Items {
			var it obsItem
			if err := attributevalue.UnmarshalMap(raw, &it); err != nil {
				return nil, fmt.Errorf("decode observation: %w", err)
			}
			out = append(out, netatmo.Observation{
				Timestamp: it.Timestamp, Temperature: it.Temperature, Humidity: it.Humidity,
				WindSpeed: it.WindSpeed, WindAngle: it.WindAngle, GustSpeed: it.GustSpeed, GustAngle: it.GustAngle,
				Rain: it.Rain, Has: netatmo.Fields(it.Has),
			})
		}
		if len(res.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = res.LastEvaluatedKey
	}
}

// SaveObservations writes the given observations, one item per hour.
func (s *Store) SaveObservations(ctx context.Context, obs []netatmo.Observation) error {
	expires := s.now().Add(s.obsTTL).Unix()
	for _, o := range obs {
		// Written as they are held: with Has resolved, so the stored form
		// never relies on the zero-value-means-everything convention.
		it := obsItem{
			PK: s.pk, SK: obsSK(o.Timestamp), TTL: expires, Timestamp: o.Timestamp,
			Temperature: o.Temperature, Humidity: o.Humidity,
			WindSpeed: o.WindSpeed, WindAngle: o.WindAngle, GustSpeed: o.GustSpeed, GustAngle: o.GustAngle,
			Rain: o.Rain, Has: uint8(o.Provides()),
		}
		if err := s.put(ctx, it); err != nil {
			return fmt.Errorf("save observation %d: %w", o.Timestamp, err)
		}
	}
	return nil
}

// LoadMeta returns the staleness bookkeeping, or zero values if the station
// has never reported.
func (s *Store) LoadMeta(ctx context.Context) (lastMeasured int64, updatedAt time.Time, err error) {
	var m metaItem
	switch err := s.get(ctx, skMeta, &m); {
	case errors.Is(err, ErrNotFound):
		return 0, time.Time{}, nil
	case err != nil:
		return 0, time.Time{}, err
	}
	if m.UpdatedAt != 0 {
		updatedAt = time.Unix(m.UpdatedAt, 0)
	}
	return m.LastMeasured, updatedAt, nil
}

// SaveMeta records the staleness bookkeeping.
func (s *Store) SaveMeta(ctx context.Context, lastMeasured int64, updatedAt time.Time) error {
	m := metaItem{PK: s.pk, SK: skMeta, LastMeasured: lastMeasured}
	if !updatedAt.IsZero() {
		m.UpdatedAt = updatedAt.Unix()
	}
	return s.put(ctx, m)
}

// LoadState loads everything a time series needs to resume.
func (s *Store) LoadState(ctx context.Context) (State, error) {
	obs, err := s.LoadObservations(ctx)
	if err != nil {
		return State{}, fmt.Errorf("load observations: %w", err)
	}
	last, updated, err := s.LoadMeta(ctx)
	if err != nil {
		return State{}, fmt.Errorf("load meta: %w", err)
	}
	return State{Observations: obs, LastMeasured: last, UpdatedAt: updated}, nil
}

// --- current reading and forecast ---

// SaveCurrent stores the latest live reading.
func (s *Store) SaveCurrent(ctx context.Context, cur *netatmo.Current) error {
	return s.putBlob(ctx, skCurrent, cur)
}

// LoadCurrent returns the latest live reading, or ErrNotFound.
func (s *Store) LoadCurrent(ctx context.Context) (*netatmo.Current, error) {
	var cur netatmo.Current
	if err := s.getBlob(ctx, skCurrent, &cur); err != nil {
		return nil, err
	}
	return &cur, nil
}

// SaveForecast stores the computed forecast.
func (s *Store) SaveForecast(ctx context.Context, fc *forecast.Forecast) error {
	return s.putBlob(ctx, skForecast, fc)
}

// LoadForecast returns the stored forecast, or ErrNotFound.
func (s *Store) LoadForecast(ctx context.Context) (*forecast.Forecast, error) {
	var fc forecast.Forecast
	if err := s.getBlob(ctx, skForecast, &fc); err != nil {
		return nil, err
	}
	return &fc, nil
}

// --- OAuth token ---

// TokenStore adapts the Store to netatmo.TokenStore, so the OAuth token (and
// its rotating refresh token) lives in the table rather than on a disk the
// function doesn't have.
func (s *Store) TokenStore() netatmo.TokenStore { return tokenStore{s} }

type tokenStore struct{ s *Store }

// tokenTimeout bounds one token read or write: netatmo.TokenStore has no
// context, and a hung call must not outlive a Lambda invocation.
const tokenTimeout = 5 * time.Second

func (t tokenStore) Load() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTimeout)
	defer cancel()
	var tok oauth2.Token
	if err := t.s.getBlob(ctx, skToken, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func (t tokenStore) Save(tok *oauth2.Token) error {
	ctx, cancel := context.WithTimeout(context.Background(), tokenTimeout)
	defer cancel()
	return t.s.putBlob(ctx, skToken, tok)
}
