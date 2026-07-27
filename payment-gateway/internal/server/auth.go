package server

import (
	"net/http"
	"strings"
)

func (s *Server) chainFXAuthContext(r *http.Request) chainFXAuth {
	key := chainFXAPIKey(r)
	auth := s.chainFXAuthForKey(key)
	if auth.Valid {
		return auth
	}
	if s == nil || s.db == nil || key == "" {
		return chainFXAuth{}
	}
	validated, err := s.db.ValidateDeveloperAPIKey(r.Context(), key)
	if err == nil && validated != nil {
		return chainFXAuth{
			Valid:         true,
			Sandbox:       validated.Environment != "production",
			Mode:          validated.Environment,
			ProjectID:     validated.ProjectID,
			APIKeyID:      validated.KeyID,
			APIKeyLogHash: validated.LogHash,
			Scopes:        validated.Scopes,
		}
	}
	admin, err := s.db.ValidateAdminSession(r.Context(), key)
	if err != nil || admin == nil {
		return chainFXAuth{}
	}
	return chainFXAuth{
		Valid:   true,
		Sandbox: false,
		Mode:    "developer-login",
		Scopes:  []string{"developer:read", "developer:buy_sell", "webhooks:read"},
	}
}

func (s *Server) chainFXAuthForKey(key string) chainFXAuth {
	if key == "" {
		return chainFXAuth{}
	}
	if strings.HasPrefix(key, "sk_test_") || csvContains(s.cfg.ChainFXTestSecretKeys, key) {
		return chainFXAuth{Valid: true, Sandbox: true, Mode: "test"}
	}
	if csvContains(s.cfg.ChainFXLiveSecretKeys, key) {
		return chainFXAuth{Valid: true, Mode: "live"}
	}
	if strings.HasPrefix(key, "sk_live_") && !s.cfg.ChainFXRequireAPIKey && !s.cfg.IsProduction() {
		return chainFXAuth{Valid: true, Mode: "live-dev"}
	}
	return chainFXAuth{}
}

func chainFXAPIKey(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if key := strings.TrimSpace(r.Header.Get("X-Api-Key")); key != "" {
		return key
	}
	return strings.TrimSpace(r.URL.Query().Get("apiKey"))
}

func chainFXAPIKeyFromHeader(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}
