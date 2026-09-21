package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/rehabaam/mycast/forecast"
	"github.com/rehabaam/mycast/netatmo"
	"github.com/rehabaam/mycast/store"
)

// Server is the HTTP API server.
type Server struct {
	port   string
	engine *forecast.Engine
	client *netatmo.Client
	ts     *store.TimeSeries
}

func NewServer(port string, engine *forecast.Engine, client *netatmo.Client, ts *store.TimeSeries) *Server {
	return &Server{port: port, engine: engine, client: client, ts: ts}
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/current", s.handleCurrent)
	mux.HandleFunc("/forecast", s.handleForecast)
	mux.HandleFunc("/debug", s.handleDebug)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", s.port),
		Handler:      loggingMiddleware(jsonMiddleware(mux)),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("API server listening on :%s", s.port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
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
