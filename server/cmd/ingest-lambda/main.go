// Command ingest-lambda is the scheduled half of mycast's AWS deployment: on
// each invocation it fetches the station's reading, folds it into the stored
// time series, recomputes the forecast and saves the results to DynamoDB.
//
// The Netatmo OAuth token lives in the same table. Run cmd/mycast-auth once to
// authorise; after that the function keeps the (rotating) token current.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
	_ "time/tzdata" // embed the timezone database: the Lambda image may not have one

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"golang.org/x/oauth2"

	"github.com/rehabaam/mycast/config"
	"github.com/rehabaam/mycast/dynamo"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/serverless"
)

// netatmoTimeout bounds every call to Netatmo, token refresh included, so a
// hung upstream fails the invocation instead of running into the function
// timeout.
const netatmoTimeout = 20 * time.Second

type deps struct {
	cfg          *config.Config
	repo         *dynamo.Store
	clientID     string
	clientSecret string
}

var cold struct {
	deps *deps
	err  error
	done bool
}

// coldStart runs once per execution environment and keeps its work for warm
// invocations: AWS clients and secrets do not change between them.
func coldStart(ctx context.Context) (*deps, error) {
	if cold.done {
		return cold.deps, cold.err
	}
	cold.done = true
	cold.deps, cold.err = initDeps(ctx)
	return cold.deps, cold.err
}

func initDeps(ctx context.Context) (*deps, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	table := os.Getenv("MYCAST_TABLE")
	prefix := os.Getenv("MYCAST_SSM_PREFIX")
	if table == "" || prefix == "" {
		return nil, errors.New("MYCAST_TABLE and MYCAST_SSM_PREFIX must be set")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	secrets, err := serverless.LoadSecrets(ctx, ssm.NewFromConfig(awsCfg), prefix, "netatmo-client-id", "netatmo-client-secret")
	if err != nil {
		return nil, err
	}

	// Observations are kept for the history window plus a day's margin.
	ttl := time.Duration(cfg.HistoryDays+1) * 24 * time.Hour
	return &deps{
		cfg:          cfg,
		repo:         dynamo.New(dynamodb.NewFromConfig(awsCfg), table, dynamo.WithObservationTTL(ttl)),
		clientID:     secrets["netatmo-client-id"],
		clientSecret: secrets["netatmo-client-secret"],
	}, nil
}

func handler(ctx context.Context) error {
	d, err := coldStart(ctx)
	if err != nil {
		return err
	}

	// The OAuth library takes its HTTP client from the context, which is how
	// the token refresh gets a timeout too.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: netatmoTimeout})

	auth := netatmo.NewAuthenticatorWithStore(d.clientID, d.clientSecret, d.cfg.RedirectURL, d.repo.TokenStore())
	httpClient, err := auth.StoredHTTPClient(ctx)
	if err != nil {
		if errors.Is(err, netatmo.ErrReauthorize) {
			log.Printf("ingest: Netatmo authorisation is missing or revoked — run `mycast-auth` to authorise again: %v", err)
		}
		return fmt.Errorf("netatmo client: %w", err)
	}
	httpClient.Timeout = netatmoTimeout

	ing := &serverless.Ingestor{
		Repo: d.repo,
		Source: netatmo.NewClient(httpClient, netatmo.Config{
			StationID:       d.cfg.StationID,
			OutdoorModuleID: d.cfg.OutdoorModuleID,
			WindModuleID:    d.cfg.WindModuleID,
			RainModuleID:    d.cfg.RainModuleID,
		}),
		Config: d.cfg,
		Now:    time.Now,
	}

	res, err := ing.Run(ctx)
	if err != nil {
		return err
	}
	log.Printf("ingest: ok — caught up %d h, saved %d observations, current=%v meta=%v forecast=%v",
		res.CaughtUp, res.ObservationsSaved, res.CurrentSaved, res.MetaSaved, res.ForecastSaved)
	return nil
}

func main() {
	lambda.Start(handler)
}
