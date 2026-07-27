package mobile

import (
	"database/sql"
	"log/slog"
	"net/http"
)

// handleListNotifications — GET /api/mobile/notifications
func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	list, err := mobileDB(s.db).ListNotifications(r.Context(), uid, 50)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": list, "count": len(list)})
}

// handleGetNotification — GET /api/mobile/notifications/{id}
func (s *Server) handleGetNotification(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	notification, err := mobileDB(s.db).GetNotification(r.Context(), uid, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "notificacao nao encontrada"})
			return
		}
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notification": notification})
}

// handleMarkNotificationsRead — PUT /api/mobile/notifications/read
func (s *Server) handleMarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		IDs []string `json:"ids"` // empty = mark all
	}
	_ = decodeJSON(r, &req)
	if err := mobileDB(s.db).MarkNotificationsRead(r.Context(), uid, req.IDs); err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteNotification — DELETE /api/mobile/notifications/{id}
func (s *Server) handleDeleteNotification(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	if err := mobileDB(s.db).DeleteNotification(r.Context(), uid, id); err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRegisterPushToken — POST /api/mobile/notifications/token
func (s *Server) handleRegisterPushToken(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		FCMToken   string `json:"fcm_token"`
		APNSToken  string `json:"apns_token"`
		DeviceName string `json:"device_name"`
		DeviceType string `json:"device_type"` // android | ios
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	if req.FCMToken == "" && req.APNSToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "fcm_token ou apns_token obrigatório"})
		return
	}
	if err := mobileDB(s.db).UpsertDevice(r.Context(), uid, req.DeviceName, req.DeviceType, req.FCMToken, req.APNSToken); err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
