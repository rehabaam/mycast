package api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/rehabaam/mycast/netatmo"
)

type healthResponse struct {
	Status string `json:"status"`
	Time   string `json:"time"`
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status: "ok",
		Time:   time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleCurrent(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.current.Latest()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "no station reading available yet")
		return
	}

	writeJSON(w, http.StatusOK, newCurrentResponse(cur, s.isStale(cur)))
}

// isStale reports whether the cached reading should not be presented as
// current. There are two ways for that to happen, and both have to be checked:
// fetches from Netatmo may be failing, so the cached reading is old (FetchedAt);
// or fetches may succeed while the outdoor module itself has gone quiet, so
// the service keeps re-fetching the same old measurement (OutdoorTimestamp).
func (s *Server) isStale(cur *netatmo.Current) bool {
	now := s.now()
	if now.Sub(cur.FetchedAt) > s.staleAfter {
		return true
	}
	return cur.OutdoorAvailable && now.Sub(time.Unix(cur.OutdoorTimestamp, 0)) > s.staleAfter
}

func (s *Server) handleForecast(w http.ResponseWriter, r *http.Request) {
	// Fresh() forces a synchronous recompute if the cached forecast has
	// gone stale (e.g. a stalled scheduler), so /forecast never silently
	// serves a forecast pinned to the wrong day — and always returns a
	// non-nil result, unlike the raw cache accessor.
	fc := s.engine.Fresh()
	writeJSON(w, http.StatusOK, fc)
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	obs := s.ts.All()
	type summary struct {
		Count          int       `json:"observation_count"`
		FirstTimestamp int64     `json:"first_timestamp,omitempty"`
		LastTimestamp  int64     `json:"last_timestamp,omitempty"`
		SampleTemps    []float64 `json:"sample_temps_last_5"`
	}
	sum := summary{Count: len(obs), SampleTemps: []float64{}}
	if len(obs) > 0 {
		sum.FirstTimestamp = obs[0].Timestamp
		sum.LastTimestamp = obs[len(obs)-1].Timestamp
		start := len(obs) - 5
		if start < 0 {
			start = 0
		}
		for _, o := range obs[start:] {
			sum.SampleTemps = append(sum.SampleTemps, o.Temperature)
		}
	}
	writeJSON(w, http.StatusOK, sum)
}

// writeJSON encodes v before writing anything, so an encoding failure can
// still be reported as a 500 instead of a truncated 200.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Printf("api: encode response: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("{\"error\":\"internal error\"}\n"))
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
