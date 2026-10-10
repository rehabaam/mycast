// Command api-lambda is the request-serving half of mycast's AWS deployment:
// a Lambda Function URL that serves /forecast, /current, /health and /debug
// from what the ingest function stored in DynamoDB. It never calls Netatmo.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/rehabaam/mycast/api"
	"github.com/rehabaam/mycast/config"
	"github.com/rehabaam/mycast/dynamo"
	"github.com/rehabaam/mycast/lambdahttp"
	"github.com/rehabaam/mycast/serverless"
)

func build(ctx context.Context) (*api.Server, error) {
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
	// LoadSecrets rejects an empty value, so this function can never come up
	// serving an open API on a public URL.
	secrets, err := serverless.LoadSecrets(ctx, ssm.NewFromConfig(awsCfg), prefix, "api-token")
	if err != nil {
		return nil, err
	}

	reader := dynamo.New(dynamodb.NewFromConfig(awsCfg), table).Reader(cfg.StaleAfter())
	return api.NewServerWith("", cfg.StaleAfter(), reader, reader, reader).RequireBearerToken(secrets["api-token"]), nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	srv, err := build(ctx)
	cancel()
	if err != nil {
		// Fail the cold start loudly rather than serve anything half-configured.
		log.Fatalf("api-lambda: %v", err)
	}
	lambda.Start(lambdahttp.FunctionURL(srv.Handler()))
}
