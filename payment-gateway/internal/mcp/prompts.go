package mcp

import (
	"net/http"
)

// handlePromptsList exposes the prompt template catalog defined in
// internal/agents/prompts.go so MCP clients can discover reusable prompts
// for market analysis, recommendations, anomaly detection, price
// prediction and transaction summaries.
func (s *Server) handlePromptsList(w http.ResponseWriter, r *http.Request) {
	writeCachedJSON(w, http.StatusOK, s.promptsJSON)
}
