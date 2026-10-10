package serverless

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// ParameterReader is the part of *ssm.Client used to read secrets.
type ParameterReader interface {
	GetParameters(ctx context.Context, in *ssm.GetParametersInput, opts ...func(*ssm.Options)) (*ssm.GetParametersOutput, error)
}

// LoadSecrets reads the named SecureString parameters under prefix (for
// example "/mycast/") in one call and returns them keyed by short name.
//
// Secrets are kept in Parameter Store rather than in the function's
// environment, where anyone who can read the function's configuration could
// see them. Errors name parameters, never values.
func LoadSecrets(ctx context.Context, client ParameterReader, prefix string, names ...string) (map[string]string, error) {
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	full := make([]string, len(names))
	for i, n := range names {
		full[i] = prefix + n
	}

	out, err := client.GetParameters(ctx, &ssm.GetParametersInput{Names: full, WithDecryption: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("read parameters under %q: %w", prefix, err)
	}
	if len(out.InvalidParameters) > 0 {
		return nil, fmt.Errorf("parameters not found: %s", strings.Join(out.InvalidParameters, ", "))
	}

	got := make(map[string]string, len(out.Parameters))
	for _, p := range out.Parameters {
		if p.Name != nil && p.Value != nil {
			got[strings.TrimPrefix(*p.Name, prefix)] = *p.Value
		}
	}
	for _, n := range names {
		if got[n] == "" {
			return nil, fmt.Errorf("parameter %q is empty", prefix+n)
		}
	}
	return got, nil
}
