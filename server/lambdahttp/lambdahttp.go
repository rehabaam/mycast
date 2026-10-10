// Package lambdahttp runs an ordinary net/http handler behind a Lambda
// Function URL, so the API's routes, middleware and error shapes are the same
// code whether mycast runs as a process or as a function.
package lambdahttp

import (
	"context"
	"encoding/base64"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// FunctionURL adapts h to the handler signature lambda.Start expects for a
// Function URL (payload format 2.0).
func FunctionURL(h http.Handler) func(context.Context, events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	return func(ctx context.Context, ev events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
		req, err := newRequest(ctx, ev)
		if err != nil {
			return events.LambdaFunctionURLResponse{
				StatusCode: http.StatusBadRequest,
				Headers:    map[string]string{"Content-Type": "application/json"},
				Body:       `{"error":"bad request"}`,
			}, nil
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return newResponse(rec), nil
	}
}

func newRequest(ctx context.Context, ev events.LambdaFunctionURLRequest) (*http.Request, error) {
	var body io.Reader = strings.NewReader(ev.Body)
	if ev.IsBase64Encoded {
		raw, err := base64.StdEncoding.DecodeString(ev.Body)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(string(raw))
	}

	target := ev.RawPath
	if target == "" {
		target = "/"
	}
	if ev.RawQueryString != "" {
		target += "?" + ev.RawQueryString
	}

	req, err := http.NewRequestWithContext(ctx, ev.RequestContext.HTTP.Method, target, body)
	if err != nil {
		return nil, err
	}
	for k, v := range ev.Headers {
		req.Header.Set(k, v)
	}
	req.Host = ev.Headers["host"]
	req.RemoteAddr = ev.RequestContext.HTTP.SourceIP
	return req, nil
}

func newResponse(rec *httptest.ResponseRecorder) events.LambdaFunctionURLResponse {
	res := rec.Result()
	defer res.Body.Close()

	headers := make(map[string]string, len(res.Header))
	for k, vs := range res.Header {
		headers[k] = strings.Join(vs, ",")
	}

	out := events.LambdaFunctionURLResponse{StatusCode: res.StatusCode, Headers: headers}
	raw := rec.Body.Bytes()
	if isText(res.Header.Get("Content-Type")) {
		out.Body = string(raw)
	} else {
		out.Body = base64.StdEncoding.EncodeToString(raw)
		out.IsBase64Encoded = true
	}
	return out
}

// isText reports whether a body can travel as a plain string. An absent
// Content-Type counts as text only for an empty body, which both paths treat
// the same, so unknown types are safely base64 encoded.
func isText(contentType string) bool {
	if contentType == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json")
}
