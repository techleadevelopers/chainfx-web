package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

func jsonRawToMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func decodeJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dest)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, err error) {
	slog.Error("Erro HTTP", "error", err)
	writeAPIError(w, nil, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.")
}

func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	payload := map[string]any{
		"code":    code,
		"message": message,
		"error":   message,
	}
	if r != nil {
		if id := requestID(r); id != "" {
			payload["requestId"] = id
		}
	}
	writeJSON(w, status, payload)
}
