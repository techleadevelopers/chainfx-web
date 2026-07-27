package server

import (
	"net/http"

	"payment-gateway/internal/database"
)

func (s *Server) handleGetAgentPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	policy, err := s.db.GetAgentPolicy(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if policy == nil {
		writeAPIError(w, r, http.StatusNotFound, "AGENT_POLICY_NOT_FOUND", "Agent policy not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": policy})
}

func (s *Server) handleUpdateAgentPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req database.AgentPolicyInput
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body must be valid JSON.")
		return
	}
	policy, err := s.db.UpsertAgentPolicy(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "AGENT_POLICY_UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": policy})
}
