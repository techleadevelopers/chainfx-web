package mobile

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

// anonymousUserID is the sentinel UUID used when no authenticated user is present.
// It keeps the (operation_id, user_id) PK consistent.
const anonymousUserID = "00000000-0000-0000-0000-000000000000"

// ─── context key ─────────────────────────────────────────────────────────────

type ctxKeyIdempotency struct{}

func withIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, ctxKeyIdempotency{}, key)
}

func idempotencyKeyFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyIdempotency{}).(string)
	return v
}

// ─── errors ───────────────────────────────────────────────────────────────────

// ErrIdempotencyStore is returned when the idempotency table is unreachable.
// For money-moving operations the middleware fails safe (returns 503) rather
// than proceeding without a deduplication guarantee.
var ErrIdempotencyStore = errors.New("idempotency store unavailable")

// ─── middleware ───────────────────────────────────────────────────────────────

// requireIdempotency wraps a handler to enforce idempotency.
//
// Clients send an Idempotency-Key header (UUID v4 recommended, max 128 chars).
// Within 24 h, a duplicate (key, user_id) pair returns the cached outcome
// without re-executing the protected handler.
//
// Key design decisions:
//   - Uniqueness is (operation_id, user_id) — not global — preventing cross-user replay.
//   - State transitions use SELECT FOR UPDATE inside a single transaction to be race-safe.
//   - On idempotency-store failure the middleware returns 503 (fail-safe), not open.
func (s *Server) requireIdempotency(opType string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			key = strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
		}
		if key == "" {
			key = newIdempotencyKey()
			w.Header().Set("Idempotency-Key", key)
			w.Header().Set("Idempotency-Key-Source", "server-generated")
		}
		if len(key) > 128 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "Idempotency-Key muito longo (máx 128 chars)",
			})
			return
		}

		uid := resolvedUID(userIDFromCtx(r))

		if err := s.runIdempotencyTransaction(r.Context(), w, key, uid, opType); err != nil {
			if errors.Is(err, errProceed) {
				// Transaction committed — call the real handler
				w.Header().Set("Idempotency-Key", key)
				nextReq := r.WithContext(withIdempotencyKey(r.Context(), key))
				rec := &idempotencyResponseRecorder{ResponseWriter: w}
				next(rec, nextReq)
				status := rec.status
				if status == 0 {
					status = http.StatusOK
				}
				if status >= 200 && status < 300 {
					s.markIdempotencyComplete(nextReq, rec.body.String())
				} else {
					s.markIdempotencyFailed(nextReq, rec.body.String())
				}
			}
			// All other errors were already written to w inside the transaction
			return
		}
	}
}

type idempotencyResponseRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *idempotencyResponseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *idempotencyResponseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	_, _ = r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// errProceed is a sentinel used to signal "transaction ok, call next handler".
var errProceed = errors.New("proceed")

func (s *Server) runIdempotencyTransaction(
	ctx context.Context, w http.ResponseWriter,
	key, uid, opType string,
) error {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "serviço de idempotência indisponível, tente novamente",
		})
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Upsert the (key, user_id) pair — ON CONFLICT DO NOTHING to handle duplicates
	_, err = tx.ExecContext(ctx, `
		INSERT INTO operation_ids
		    (operation_id, user_id, operation_type, status, expires_at)
		VALUES ($1, $2::uuid, $3, 'pending', NOW() + INTERVAL '24 hours')
		ON CONFLICT (operation_id, user_id) DO NOTHING`,
		key, uid, opType)
	if err != nil {
		// Table missing or other DB error — fail safe
		_ = tx.Rollback()
		committed = true
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "serviço de idempotência indisponível, tente novamente",
		})
		return err
	}

	// Lock the row for this (key, user_id) — prevents concurrent state changes
	var status, resultRef string
	var completedAt *time.Time
	row := tx.QueryRowContext(ctx, `
		SELECT status, COALESCE(result_ref,''), completed_at
		  FROM operation_ids
		 WHERE operation_id=$1 AND user_id=$2::uuid
		FOR UPDATE`,
		key, uid)

	if err := row.Scan(&status, &resultRef, &completedAt); err != nil {
		_ = tx.Rollback()
		committed = true
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "serviço de idempotência indisponível, tente novamente",
		})
		return err
	}

	switch status {
	case "completed":
		if err := tx.Commit(); err != nil {
			committed = true
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "erro interno"})
			return err
		}
		committed = true
		ts := ""
		if completedAt != nil {
			ts = completedAt.Format(time.RFC3339)
		}
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, map[string]any{
			"idempotent":   true,
			"operation_id": key,
			"result_ref":   resultRef,
			"completed_at": ts,
		})
		return nil // NOT errProceed — response already written

	case "processing":
		if err := tx.Commit(); err != nil {
			committed = true
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "erro interno"})
			return err
		}
		committed = true
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":        "operação em andamento com esta Idempotency-Key",
			"operation_id": key,
		})
		return nil // NOT errProceed

		// "failed" and "pending" → retry allowed, fall through
	}

	// Transition to 'processing' atomically within the same transaction
	if _, err := tx.ExecContext(ctx,
		"UPDATE operation_ids SET status='processing' WHERE operation_id=$1 AND user_id=$2::uuid",
		key, uid); err != nil {
		_ = tx.Rollback()
		committed = true
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "serviço de idempotência indisponível, tente novamente",
		})
		return err
	}

	if err := tx.Commit(); err != nil {
		committed = true
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "erro interno"})
		return err
	}
	committed = true
	return errProceed
}

// markIdempotencyComplete records success so future duplicates replay 200.
func (s *Server) markIdempotencyComplete(r *http.Request, resultRef string) {
	key := idempotencyKeyFromCtx(r.Context())
	if key == "" {
		return
	}
	uid := resolvedUID(userIDFromCtx(r))
	_, _ = s.db.SQL.ExecContext(r.Context(), `
		UPDATE operation_ids
		   SET status='completed', result_ref=$1, completed_at=NOW()
		 WHERE operation_id=$2 AND user_id=$3::uuid`,
		resultRef, key, uid)
}

// markIdempotencyFailed records failure — next attempt may retry.
func (s *Server) markIdempotencyFailed(r *http.Request, reason string) {
	key := idempotencyKeyFromCtx(r.Context())
	if key == "" {
		return
	}
	uid := resolvedUID(userIDFromCtx(r))
	_, _ = s.db.SQL.ExecContext(r.Context(), `
		UPDATE operation_ids
		   SET status='failed', result_ref=$1, completed_at=NOW()
		 WHERE operation_id=$2 AND user_id=$3::uuid`,
		reason, key, uid)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func resolvedUID(uid string) string {
	if uid == "" {
		return anonymousUserID
	}
	return uid
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
