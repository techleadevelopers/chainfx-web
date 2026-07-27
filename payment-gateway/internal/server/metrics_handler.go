package server

import (
	"net/http"

	"payment-gateway/internal/metrics"
)

// handleMetrics serves Prometheus-compatible metrics at GET /metrics.
// Protected by admin bearer auth — not exposed to the public internet.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	metrics.Handler().ServeHTTP(w, r)
}
