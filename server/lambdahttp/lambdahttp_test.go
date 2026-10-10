package lambdahttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/rehabaam/mycast/api"
	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
)

func urlEvent(method, path, query string, headers map[string]string) events.LambdaFunctionURLRequest {
	ev := events.LambdaFunctionURLRequest{RawPath: path, RawQueryString: query, Headers: headers}
	ev.RequestContext.HTTP.Method = method
	ev.RequestContext.HTTP.SourceIP = "203.0.113.9"
	return ev
}

func TestRequestFieldsReachTheHandler(t *testing.T) {
	var got *http.Request
	var body string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	ev := urlEvent("POST", "/a/b", "x=1&y=two%20words", map[string]string{"host": "abc.lambda-url.eu-north-1.on.aws", "authorization": "Bearer t", "x-custom": "v"})
	ev.Body = "payload"

	res, err := FunctionURL(h)(context.Background(), ev)

	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "POST" || got.URL.Path != "/a/b" || got.URL.Query().Get("y") != "two words" || got.URL.Query().Get("x") != "1" {
		t.Errorf("request = %s %s", got.Method, got.URL)
	}
	if got.Header.Get("Authorization") != "Bearer t" || got.Header.Get("X-Custom") != "v" {
		t.Errorf("headers = %v", got.Header)
	}
	if got.Host != "abc.lambda-url.eu-north-1.on.aws" || got.RemoteAddr != "203.0.113.9" {
		t.Errorf("host/remote = %q / %q", got.Host, got.RemoteAddr)
	}
	if body != "payload" {
		t.Errorf("body = %q", body)
	}
	if res.StatusCode != 200 || res.Body != `{"ok":true}` || res.IsBase64Encoded {
		t.Errorf("response = %+v", res)
	}
}

func TestBase64RequestBodiesAreDecoded(t *testing.T) {
	var body string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	})
	ev := urlEvent("POST", "/", "", nil)
	ev.Body = base64.StdEncoding.EncodeToString([]byte{0x00, 0xff, 'h', 'i'})
	ev.IsBase64Encoded = true

	if _, err := FunctionURL(h)(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if body != "\x00\xffhi" {
		t.Errorf("body = %q", body)
	}

	ev.Body = "!!not base64!!"
	res, _ := FunctionURL(h)(context.Background(), ev)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", res.StatusCode)
	}
}

func TestBinaryResponsesAreBase64EncodedAndJSONIsNot(t *testing.T) {
	bin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte{0, 1, 2, 0xff})
	})
	res, _ := FunctionURL(bin)(context.Background(), urlEvent("GET", "/", "", nil))
	if !res.IsBase64Encoded {
		t.Fatal("binary body was not marked base64")
	}
	if raw, _ := base64.StdEncoding.DecodeString(res.Body); string(raw) != "\x00\x01\x02\xff" {
		t.Errorf("decoded body = %v", raw)
	}

	txt := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "hello")
	})
	if res, _ = FunctionURL(txt)(context.Background(), urlEvent("GET", "/", "", nil)); res.IsBase64Encoded || res.Body != "hello" {
		t.Errorf("text response = %+v", res)
	}
}

func TestStatusAndHeadersAreCarried(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("Allow", "GET")
		w.Header().Add("Allow", "HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprint(w, `{"error":"x"}`)
	})
	res, _ := FunctionURL(h)(context.Background(), urlEvent("POST", "/x", "", nil))
	if res.StatusCode != 405 || res.Headers["Allow"] != "GET,HEAD" || res.Headers["Content-Type"] != "application/json" {
		t.Errorf("response = %+v", res)
	}
}

// --- the real API behind the adapter ---

type fixedForecast struct{ fc *forecast.Forecast }

func (f fixedForecast) Forecast(context.Context) (*forecast.Forecast, error) { return f.fc, nil }

type fixedCurrent struct{ cur *netatmo.Current }

func (f fixedCurrent) Latest() (*netatmo.Current, bool) { return f.cur, f.cur != nil }

type noObs struct{}

func (noObs) All() []netatmo.Observation { return nil }

func TestTheAPIRunsUnchangedBehindAFunctionURL(t *testing.T) {
	srv := api.NewServerWith("", time.Hour,
		fixedForecast{&forecast.Forecast{StationID: "st", Model: "m", Days: []forecast.DayForecast{}, Stale: true}},
		fixedCurrent{&netatmo.Current{FetchedAt: time.Now(), OutdoorAvailable: true, OutdoorTimestamp: time.Now().Unix(), OutdoorTemp: 9}},
		noObs{},
	).RequireBearerToken("s3cret")
	handler := FunctionURL(srv.Handler())
	call := func(method, path, auth string) events.LambdaFunctionURLResponse {
		h := map[string]string{}
		if auth != "" {
			h["authorization"] = auth
		}
		res, err := handler(context.Background(), urlEvent(method, path, "", h))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := call("GET", "/forecast", ""); res.StatusCode != 401 {
		t.Errorf("no token: %d, want 401", res.StatusCode)
	}
	if res := call("GET", "/forecast", "Bearer wrong"); res.StatusCode != 401 {
		t.Errorf("wrong token: %d, want 401", res.StatusCode)
	}

	res := call("GET", "/forecast", "Bearer s3cret")
	var fc map[string]any
	if res.StatusCode != 200 || json.Unmarshal([]byte(res.Body), &fc) != nil || fc["station_id"] != "st" || fc["stale"] != true {
		t.Fatalf("/forecast = %d %s", res.StatusCode, res.Body)
	}
	if res.Headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q", res.Headers["Content-Type"])
	}

	res = call("GET", "/current", "Bearer s3cret")
	var cur map[string]any
	if res.StatusCode != 200 || json.Unmarshal([]byte(res.Body), &cur) != nil || cur["outdoor_temp_c"] != 9.0 || cur["stale"] != false {
		t.Errorf("/current = %d %s", res.StatusCode, res.Body)
	}

	if res := call("GET", "/nope", "Bearer s3cret"); res.StatusCode != 404 {
		t.Errorf("/nope = %d, want 404", res.StatusCode)
	}
	if res := call("POST", "/forecast", "Bearer s3cret"); res.StatusCode != 405 {
		t.Errorf("POST /forecast = %d, want 405", res.StatusCode)
	}
}
