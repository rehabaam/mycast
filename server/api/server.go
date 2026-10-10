package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/store"
)

// CurrentSource supplies the latest station reading. It must answer from
// memory: the API never calls Netatmo on behalf of a request, so clients can't
// drive upstream traffic.
type CurrentSource interface {
	Latest() (*netatmo.Current, bool)
}

// ForecastSource supplies the forecast to serve. It is what lets the same
// routes run against a live engine (long-running process) or a stored
// forecast (serverless): the API does not care which.
type ForecastSource interface {
	Forecast(ctx context.Context) (*forecast.Forecast, error)
}

// ObservationSource supplies the stored observations for /debug.
type ObservationSource interface {
	All() []netatmo.Observation
}

// engineSource serves the engine's live-checked forecast.
type engineSource struct{ engine *forecast.Engine }

func (s engineSource) Forecast(context.Context) (*forecast.Forecast, error) {
	return s.engine.Fresh(), nil
}

// Server is the HTTP API server.
type Server struct {
	addr       string
	staleAfter time.Duration
	forecasts  ForecastSource
	current    CurrentSource
	obs        ObservationSource
	token      string
	now        func() time.Time // replaceable in tests
}

// NewServer creates a server backed by an in-process engine and time series.
// It will listen on addr (host:port). staleAfter is how long the cached
// /current reading may go without being refreshed before the response is
// flagged stale.
func NewServer(addr string, staleAfter time.Duration, engine *forecast.Engine, current CurrentSource, ts *store.TimeSeries) *Server {
	return NewServerWith(addr, staleAfter, engineSource{engine}, current, ts)
}

// NewServerWith creates a server over arbitrary sources.
func NewServerWith(addr string, staleAfter time.Duration, forecasts ForecastSource, current CurrentSource, obs ObservationSource) *Server {
	return &Server{addr: addr, staleAfter: staleAfter, forecasts: forecasts, current: current, obs: obs, now: time.Now}
}

// RequireBearerToken makes every request carry "Authorization: Bearer <token>".
// An empty token leaves the API open, which is right for a loopback-only
// process; anything reachable from a network should set one.
func (s *Server) RequireBearerToken(token string) *Server {
	s.token = token
	return s
}

// authorized reports whether the request carries the configured token. Both
// sides are hashed first so the comparison takes the same time whatever the
// length of what was sent.
func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	got := sha256.Sum256([]byte(h[len(prefix):]))
	want := sha256.Sum256([]byte(s.token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// Handler returns the API's routes wrapped in its middleware.
func (s *Server) Handler() http.Handler {
	routes := map[string]http.HandlerFunc{
		"/health":   handleHealth,
		"/current":  s.handleCurrent,
		"/forecast": s.handleForecast,
		"/debug":    s.handleDebug,
	}

	// A single catch-all instead of ServeMux method patterns, so that 404 and
	// 405 come back as JSON like every other response.
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h, ok := routes[r.URL.Path]
		if !ok {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h(w, r)
	})

	return loggingMiddleware(jsonMiddleware(root))
}

// Listen binds the server's address. Binding is separate from Serve so the
// caller learns about a port conflict before announcing that it is running.
func (s *Server) Listen() (net.Listener, error) {
	return net.Listen("tcp", s.addr)
}

// Serve serves on ln until ctx is cancelled, then shuts down gracefully. It
// returns once the server has stopped; a nil error means a clean shutdown.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := srv.Shutdown(shutCtx)
		if serveErr := <-errCh; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return err
	}
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// loggingMiddleware logs one line per request, except those rejected for a
// missing or wrong token. The URL is public, so those are the requests anyone
// on the internet can generate at will, and a log line for each would let
// them run up a log bill for free.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status == http.StatusUnauthorized {
			return
		}
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func jsonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
