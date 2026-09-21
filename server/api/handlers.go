package api

import (
	"encoding/json"
	"net/http"
	"time"
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
	cur, err := s.client.GetCurrent()
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to fetch station data: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cur)
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
	sum := summary{Count: len(obs)}
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

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
