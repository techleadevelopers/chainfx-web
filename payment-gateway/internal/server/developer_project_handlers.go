package server

import (
	"net/http"
	"strconv"
	"strings"

	"payment-gateway/internal/database"
)

func (s *Server) handleListDeveloperProjects(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	projects, err := s.db.ListDeveloperProjects(r.Context(), consoleLimit(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleCreateDeveloperProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req database.DeveloperProjectInput
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body must be valid JSON.")
		return
	}
	project, err := s.db.CreateDeveloperProject(r.Context(), req)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PROJECT_CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": project})
}

func (s *Server) handleGetDeveloperProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	project, err := s.db.GetDeveloperProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if project == nil {
		writeAPIError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "Project not found.")
		return
	}
	keys, err := s.db.ListDeveloperAPIKeys(r.Context(), project.ID, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "apiKeys": keys})
}

func (s *Server) handleUpdateDeveloperProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req database.DeveloperProjectInput
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body must be valid JSON.")
		return
	}
	project, err := s.db.UpdateDeveloperProject(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PROJECT_UPDATE_FAILED", err.Error())
		return
	}
	if project == nil {
		writeAPIError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "Project not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project})
}

func (s *Server) handleListDeveloperAPIKeysV2(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	keys, err := s.db.ListDeveloperAPIKeys(r.Context(), strings.TrimSpace(r.URL.Query().Get("projectId")), consoleLimit(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apiKeys": keys})
}

func (s *Server) handleCreateDeveloperProjectAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req database.DeveloperAPIKeyInput
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body must be valid JSON.")
		return
	}
	created, err := s.db.CreateDeveloperAPIKey(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "API_KEY_CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleRotateDeveloperAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	created, err := s.db.RotateDeveloperAPIKey(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "API_KEY_ROTATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, created)
}

func (s *Server) handleSetDeveloperAPIKeyStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	status := strings.TrimSpace(r.PathValue("status"))
	if status == "" {
		var req struct {
			Status string `json:"status"`
		}
		_ = decodeJSON(r, &req)
		status = req.Status
	}
	key, err := s.db.SetDeveloperAPIKeyStatus(r.Context(), r.PathValue("id"), status)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "API_KEY_STATUS_FAILED", err.Error())
		return
	}
	if key == nil {
		writeAPIError(w, r, http.StatusNotFound, "API_KEY_NOT_FOUND", "API key not found.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

func parseExpirationDays(value string) int {
	days, _ := strconv.Atoi(strings.TrimSpace(value))
	return days
}
