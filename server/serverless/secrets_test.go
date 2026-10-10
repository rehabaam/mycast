package serverless

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeSSM struct {
	params map[string]string
	err    error
	gotIn  *ssm.GetParametersInput
}

func (f *fakeSSM) GetParameters(_ context.Context, in *ssm.GetParametersInput, _ ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	f.gotIn = in
	if f.err != nil {
		return nil, f.err
	}
	out := &ssm.GetParametersOutput{}
	for _, n := range in.Names {
		if v, ok := f.params[n]; ok {
			out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(n), Value: aws.String(v)})
		} else {
			out.InvalidParameters = append(out.InvalidParameters, n)
		}
	}
	return out, nil
}

func TestLoadSecretsReturnsValuesByShortNameWithDecryption(t *testing.T) {
	f := &fakeSSM{params: map[string]string{"/mycast/netatmo-client-id": "id-1", "/mycast/netatmo-client-secret": "sec-2"}}

	got, err := LoadSecrets(context.Background(), f, "/mycast/", "netatmo-client-id", "netatmo-client-secret")

	if err != nil {
		t.Fatal(err)
	}
	if got["netatmo-client-id"] != "id-1" || got["netatmo-client-secret"] != "sec-2" {
		t.Errorf("secrets = %v", got)
	}
	if f.gotIn.WithDecryption == nil || !*f.gotIn.WithDecryption {
		t.Error("SecureString parameters must be requested with decryption")
	}
	if len(f.gotIn.Names) != 2 {
		t.Errorf("made one request for %d names, want both in a single call", len(f.gotIn.Names))
	}
}

func TestLoadSecretsNamesMissingParametersButNeverValues(t *testing.T) {
	f := &fakeSSM{params: map[string]string{"/mycast/netatmo-client-id": "SUPER-SECRET-VALUE"}}

	_, err := LoadSecrets(context.Background(), f, "/mycast/", "netatmo-client-id", "api-token")

	if err == nil || !strings.Contains(err.Error(), "/mycast/api-token") {
		t.Fatalf("err = %v, want it to name the missing parameter", err)
	}
	if strings.Contains(err.Error(), "SUPER-SECRET-VALUE") {
		t.Error("an error message contains a secret value")
	}
}

func TestLoadSecretsRejectsEmptyValuesAndPropagatesErrors(t *testing.T) {
	empty := &fakeSSM{params: map[string]string{"/mycast/api-token": ""}}
	if _, err := LoadSecrets(context.Background(), empty, "/mycast/", "api-token"); err == nil {
		t.Error("an empty secret was accepted")
	}

	boom := &fakeSSM{err: errors.New("AccessDeniedException")}
	if _, err := LoadSecrets(context.Background(), boom, "/mycast/", "api-token"); err == nil {
		t.Error("a read failure was swallowed")
	}

	none, err := LoadSecrets(context.Background(), &fakeSSM{}, "/mycast/")
	if err != nil || len(none) != 0 {
		t.Errorf("LoadSecrets with no names = (%v, %v)", none, err)
	}
}
