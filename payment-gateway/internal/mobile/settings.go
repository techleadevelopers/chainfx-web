package mobile

import (
	"net/http"
	"strings"

	"payment-gateway/internal/models"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	settings, err := mobileDB(s.db).GetSettings(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DarkMode             *bool    `json:"dark_mode"`
		Language             *string  `json:"language"`
		Currency             *string  `json:"currency"`
		NotificationsEnabled *bool    `json:"notifications_enabled"`
		DailyLimit           *float64 `json:"daily_limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid payload"})
		return
	}

	uid := userIDFromCtx(r)
	settings, err := mobileDB(s.db).GetSettings(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if settings == nil {
		settings = &models.UserSettings{UserID: uid, DarkMode: true, Language: "pt-BR", Currency: "BRL", NotificationsEnabled: true, DailyLimit: 10000}
	}
	settings.UserID = uid

	if req.DarkMode != nil {
		settings.DarkMode = *req.DarkMode
	}
	if req.Language != nil {
		if lang := strings.TrimSpace(*req.Language); lang != "" {
			settings.Language = lang
		}
	}
	if req.Currency != nil {
		if currency := strings.ToUpper(strings.TrimSpace(*req.Currency)); currency != "" {
			settings.Currency = currency
		}
	}
	if req.NotificationsEnabled != nil {
		settings.NotificationsEnabled = *req.NotificationsEnabled
	}
	if req.DailyLimit != nil {
		if *req.DailyLimit <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "daily_limit deve ser maior que zero"})
			return
		}
		settings.DailyLimit = *req.DailyLimit
	}

	if err := mobileDB(s.db).UpsertSettings(r.Context(), settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleGetPreferences(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	settings, err := mobileDB(s.db).GetSettings(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, mobilePreferencesView(settings))
}

func (s *Server) handleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ThemeMode            *string `json:"theme_mode"`
		DarkMode             *bool   `json:"dark_mode"`
		AccentColor          *string `json:"accent_color"`
		Language             *string `json:"language"`
		Currency             *string `json:"currency"`
		NotificationsEnabled *bool   `json:"notifications_enabled"`
		NFCEnabled           *bool   `json:"nfc_enabled"`
		BiometryEnabled      *bool   `json:"biometry_enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid payload"})
		return
	}
	uid := userIDFromCtx(r)
	settings, err := mobileDB(s.db).GetSettings(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if settings == nil {
		settings = &models.UserSettings{UserID: uid, DarkMode: true, Language: "pt-BR", Currency: "BRL", NotificationsEnabled: true, DailyLimit: 10000}
	}
	settings.UserID = uid
	if req.DarkMode != nil {
		settings.DarkMode = *req.DarkMode
	}
	if req.ThemeMode != nil {
		switch strings.ToLower(strings.TrimSpace(*req.ThemeMode)) {
		case "light":
			settings.DarkMode = false
		case "dark":
			settings.DarkMode = true
		case "":
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "theme_mode must be light or dark"})
			return
		}
	}
	if req.Language != nil {
		if lang := strings.TrimSpace(*req.Language); lang != "" {
			settings.Language = lang
		}
	}
	if req.Currency != nil {
		if currency := strings.ToUpper(strings.TrimSpace(*req.Currency)); currency != "" {
			settings.Currency = currency
		}
	}
	if req.NotificationsEnabled != nil {
		settings.NotificationsEnabled = *req.NotificationsEnabled
	}
	if req.BiometryEnabled != nil {
		_ = mobileDB(s.db).UpdateUser(r.Context(), uid, map[string]any{"biometry_enabled": *req.BiometryEnabled})
	}
	if err := mobileDB(s.db).UpsertSettings(r.Context(), settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := mobilePreferencesView(settings)
	if req.AccentColor != nil && strings.TrimSpace(*req.AccentColor) != "" {
		out["accent_color"] = strings.TrimSpace(*req.AccentColor)
	}
	if req.NFCEnabled != nil {
		out["nfc_enabled"] = *req.NFCEnabled
	}
	if req.BiometryEnabled != nil {
		out["biometry_enabled"] = *req.BiometryEnabled
	}
	writeJSON(w, http.StatusOK, out)
}

func mobilePreferencesView(settings *models.UserSettings) map[string]any {
	if settings == nil {
		settings = &models.UserSettings{DarkMode: true, Language: "pt-BR", Currency: "BRL", NotificationsEnabled: true, DailyLimit: 10000}
	}
	mode := "light"
	if settings.DarkMode {
		mode = "dark"
	}
	return map[string]any{
		"theme_mode":            mode,
		"dark_mode":             settings.DarkMode,
		"accent_color":          "#12BBE6",
		"language":              settings.Language,
		"currency":              settings.Currency,
		"notifications_enabled": settings.NotificationsEnabled,
		"nfc_enabled":           true,
	}
}

func (s *Server) handleGetLimits(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	settings, err := mobileDB(s.db).GetSettings(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	dailyLimit := 10000.0
	if settings != nil && settings.DailyLimit > 0 {
		dailyLimit = settings.DailyLimit
	}
	usedToday := 0.0
	writeJSON(w, http.StatusOK, map[string]any{
		"daily_limit":         dailyLimit,
		"used_today":          usedToday,
		"remaining":           dailyLimit - usedToday,
		"max_per_transaction": dailyLimit,
	})
}
