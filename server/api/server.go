package api

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
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

// Server is the HTTP API server.
type Server struct {
	addr       string
	staleAfter time.Duration
	engine     *forecast.Engine
	current    CurrentSource
	ts         *store.TimeSeries
	now        func() time.Time // replaceable in tests
}

// NewServer creates a server that will listen on addr (host:port).
// staleAfter is how long the cached /current reading may go without being
// refreshed before the response is flagged stale.
func NewServer(addr string, staleAfter time.Duration, engine *forecast.Engine, current CurrentSource, ts *store.TimeSeries) *Server {
	return &Server{addr: addr, staleAfter: staleAfter, engine: engine, current: current, ts: ts, now: time.Now}
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

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func jsonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
