package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	ssePollInterval = 2 * time.Second
	sseHeartbeat    = 15 * time.Second
	sseMaxDuration  = 10 * time.Minute
)

type sseUpdate struct {
	Key     string
	Payload any
	Final   bool
}

func streamSSE(w http.ResponseWriter, r *http.Request, next func(context.Context) (sseUpdate, bool)) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	ctx, cancel := context.WithTimeout(r.Context(), sseMaxDuration)
	defer cancel()
	poll := time.NewTicker(ssePollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	var last string
	for {
		select {
		case <-ctx.Done():
			writeSSEComment(w, flusher, "closed")
			return
		case <-heartbeat.C:
			writeSSEComment(w, flusher, "heartbeat")
		case <-poll.C:
			update, ok := next(ctx)
			if !ok {
				continue
			}
			if update.Key != "" && update.Key != last {
				last = update.Key
				writeSSEData(w, flusher, update.Payload)
			}
			if update.Final {
				writeSSEComment(w, flusher, "final")
				return
			}
		}
	}
}

func writeSSEData(w http.ResponseWriter, flusher http.Flusher, payload any) {
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEComment(w http.ResponseWriter, flusher http.Flusher, comment string) {
	_, _ = fmt.Fprintf(w, ": %s\n\n", comment)
	if flusher != nil {
		flusher.Flush()
	}
}

func isFinalBuyStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "enviado", "delivered", "confirmado", "concluida", "concluída", "erro":
		return true
	default:
		return false
	}
}
