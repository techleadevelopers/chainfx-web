package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/workers"
)

type adminKYCRow struct {
	ID                string     `json:"id"`
	UserID            string     `json:"user_id"`
	Email             string     `json:"email"`
	FullName          string     `json:"full_name"`
	Level             int        `json:"level"`
	Status            string     `json:"status"`
	DocumentType      string     `json:"document_type"`
	DocumentURL       string     `json:"document_url"`
	DocumentBackURL   string     `json:"document_back_url"`
	SelfieURL         string     `json:"selfie_url"`
	FacialVideoURL    string     `json:"facial_video_url"`
	ProofOfAddressURL string     `json:"proof_of_address_url"`
	ProofOfIncomeURL  string     `json:"proof_of_income_url"`
	ReviewerNotes     string     `json:"reviewer_notes"`
	SubmittedAt       time.Time  `json:"submitted_at"`
	ReviewedAt        *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func (s *Server) handleAdminListKYC(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	if err := s.ensureAdminKYCSchema(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	status := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("status")))
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 100, 500)
	rows, err := s.db.SQL.QueryContext(r.Context(), `
		SELECT k.id::text,k.user_id::text,COALESCE(u.email,''),COALESCE(u.full_name,''),
		       k.level,k.status,COALESCE(k.document_type,''),COALESCE(k.document_url,''),
		       COALESCE(k.document_back_url,''),COALESCE(k.selfie_url,''),COALESCE(k.facial_video_url,''),
		       COALESCE(k.proof_of_address_url,''),COALESCE(k.proof_of_income_url,''),
		       COALESCE(k.reviewer_notes,''),k.submitted_at,k.reviewed_at,k.created_at,k.updated_at
		  FROM kyc_requests k
		  JOIN users u ON u.id = k.user_id
		 WHERE ($1 = '' OR k.status = $1)
		 ORDER BY k.submitted_at DESC
		 LIMIT $2`, status, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	out := []adminKYCRow{}
	for rows.Next() {
		var row adminKYCRow
		var reviewedAt sql.NullTime
		if err := rows.Scan(
			&row.ID, &row.UserID, &row.Email, &row.FullName, &row.Level, &row.Status,
			&row.DocumentType, &row.DocumentURL, &row.DocumentBackURL, &row.SelfieURL,
			&row.FacialVideoURL, &row.ProofOfAddressURL, &row.ProofOfIncomeURL,
			&row.ReviewerNotes, &row.SubmittedAt, &reviewedAt, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			writeError(w, err)
			return
		}
		if reviewedAt.Valid {
			row.ReviewedAt = &reviewedAt.Time
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out, "count": len(out)})
}

func (s *Server) handleAdminReviewKYC(w http.ResponseWriter, r *http.Request) {
	adminUser, _, ok := s.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if err := s.ensureAdminKYCSchema(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var req struct {
		Decision string `json:"decision"`
		Notes    string `json:"notes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	decision := strings.TrimSpace(strings.ToLower(req.Decision))
	if decision != "approved" && decision != "rejected" && decision != "in_review" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "decision must be approved, rejected or in_review"})
		return
	}
	row, err := s.reviewKYCRequest(r.Context(), id, decision, strings.TrimSpace(req.Notes), adminUser.Email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "kyc request not found"})
			return
		}
		writeError(w, err)
		return
	}
	if s.workers != nil && s.workers.Bus != nil {
		eventType := "kyc.manual_review"
		if decision == "approved" {
			eventType = "kyc.approved"
		} else if decision == "rejected" {
			eventType = "kyc.rejected"
		}
		s.workers.Bus.Publish(workers.Event{
			Type: eventType,
			Payload: map[string]any{
				"user_id":        row.UserID,
				"level":          row.Level,
				"request_id":     row.ID,
				"manual":         true,
				"reviewer_email": adminUser.Email,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kyc_request": row})
}

func (s *Server) reviewKYCRequest(ctx context.Context, id, decision, notes, adminEmail string) (*adminKYCRow, error) {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var userID string
	if err := tx.QueryRowContext(ctx, `SELECT user_id::text FROM kyc_requests WHERE id=$1 FOR UPDATE`, id).Scan(&userID); err != nil {
		return nil, err
	}
	userStatus := "submitted"
	reviewedAtExpr := "NULL"
	if decision == "approved" {
		userStatus = "approved"
		reviewedAtExpr = "NOW()"
	} else if decision == "rejected" {
		userStatus = "rejected"
		reviewedAtExpr = "NOW()"
	}
	note := strings.TrimSpace(notes)
	if adminEmail != "" {
		if note != "" {
			note += "\n"
		}
		note += "Manual review by " + adminEmail
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE kyc_requests
		   SET status=$2,
		       reviewer_notes=NULLIF($3,''),
		       reviewed_at=`+reviewedAtExpr+`,
		       updated_at=NOW()
		 WHERE id=$1`, id, decision, note); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET kyc_status=$2, updated_at=NOW() WHERE id=$1`, userID, userStatus); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getAdminKYCRow(ctx, id)
}

func (s *Server) getAdminKYCRow(ctx context.Context, id string) (*adminKYCRow, error) {
	var row adminKYCRow
	var reviewedAt sql.NullTime
	err := s.db.SQL.QueryRowContext(ctx, `
		SELECT k.id::text,k.user_id::text,COALESCE(u.email,''),COALESCE(u.full_name,''),
		       k.level,k.status,COALESCE(k.document_type,''),COALESCE(k.document_url,''),
		       COALESCE(k.document_back_url,''),COALESCE(k.selfie_url,''),COALESCE(k.facial_video_url,''),
		       COALESCE(k.proof_of_address_url,''),COALESCE(k.proof_of_income_url,''),
		       COALESCE(k.reviewer_notes,''),k.submitted_at,k.reviewed_at,k.created_at,k.updated_at
		  FROM kyc_requests k
		  JOIN users u ON u.id = k.user_id
		 WHERE k.id=$1`, id).Scan(
		&row.ID, &row.UserID, &row.Email, &row.FullName, &row.Level, &row.Status,
		&row.DocumentType, &row.DocumentURL, &row.DocumentBackURL, &row.SelfieURL,
		&row.FacialVideoURL, &row.ProofOfAddressURL, &row.ProofOfIncomeURL,
		&row.ReviewerNotes, &row.SubmittedAt, &reviewedAt, &row.CreatedAt, &row.UpdatedAt,
	)
	if reviewedAt.Valid {
		row.ReviewedAt = &reviewedAt.Time
	}
	return &row, err
}

func (s *Server) ensureAdminKYCSchema(ctx context.Context) error {
	_, err := s.db.SQL.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS kyc_requests (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  level INT NOT NULL DEFAULT 1,
  status VARCHAR(32) NOT NULL DEFAULT 'pending',
  document_type VARCHAR(32),
  document_url TEXT,
  selfie_url TEXT,
  proof_of_address_url TEXT,
  proof_of_income_url TEXT,
  reviewer_notes TEXT,
  submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  reviewed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE kyc_requests ADD COLUMN IF NOT EXISTS document_back_url VARCHAR(2048);
ALTER TABLE kyc_requests ADD COLUMN IF NOT EXISTS facial_video_url VARCHAR(2048);
CREATE INDEX IF NOT EXISTS idx_kyc_requests_user ON kyc_requests(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_kyc_requests_pending ON kyc_requests(status, submitted_at) WHERE status IN ('pending','pending_processing','in_review');`)
	return err
}

func parsePositiveInt(raw string, fallback, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return fallback
	}
	if max > 0 && n > max {
		return max
	}
	return n
}
