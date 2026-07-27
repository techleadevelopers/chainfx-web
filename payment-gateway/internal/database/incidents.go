package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"payment-gateway/internal/privacy"
)

const (
	IncidentStatusOpen = "open"
)

type DocumentMatchStatus string

const (
	DocumentMatchUnknown  DocumentMatchStatus = "unknown"
	DocumentMatchOK       DocumentMatchStatus = "match"
	DocumentMatchMismatch DocumentMatchStatus = "mismatch"
)

func (db *DB) OrderPixCPFMatchStatus(ctx context.Context, orderID, payerDoc string) (DocumentMatchStatus, error) {
	payerDigits := onlyDigits(payerDoc)
	if payerDigits == "" {
		return DocumentMatchUnknown, nil
	}

	if len(payerDigits) == 11 || len(payerDigits) == 14 {
		cpfHash := privacy.Hash(payerDoc, db.cfg.LGPDSecret)
		if cpfHash == "" {
			return DocumentMatchUnknown, nil
		}
		var stored string
		err := db.SQL.QueryRowContext(ctx, `
			SELECT COALESCE(pix_cpf_hash, '')
			FROM orders
			WHERE id = $1`, strings.TrimSpace(orderID)).Scan(&stored)
		if err != nil {
			return DocumentMatchUnknown, err
		}
		if stored == "" {
			return DocumentMatchUnknown, nil
		}
		if stored == cpfHash {
			return DocumentMatchOK, nil
		}
		return DocumentMatchMismatch, nil
	}

	if len(payerDigits) < 6 {
		return DocumentMatchUnknown, nil
	}

	var enc sql.NullString
	err := db.SQL.QueryRowContext(ctx, `
		SELECT op.pix_cpf_enc
		FROM orders o
		LEFT JOIN order_private op ON op.order_id = o.id
		WHERE o.id = $1`, strings.TrimSpace(orderID)).Scan(&enc)
	if err != nil {
		return DocumentMatchUnknown, err
	}
	if !enc.Valid || db.privacy == nil {
		return DocumentMatchUnknown, nil
	}
	expected, err := db.privacy.Decrypt(enc.String)
	if err != nil {
		return DocumentMatchUnknown, err
	}
	expectedDigits := onlyDigits(expected)
	if expectedDigits == "" {
		return DocumentMatchUnknown, nil
	}
	if strings.Contains(expectedDigits, payerDigits) {
		return DocumentMatchOK, nil
	}
	return DocumentMatchMismatch, nil
}

func onlyDigits(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (db *DB) OpenOrderIncident(ctx context.Context, orderID, incidentType, severity, reason string, payload any) error {
	raw, _ := json.Marshal(payload)
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO order_incidents (order_id, incident_type, severity, reason, payload, status)
		VALUES ($1,$2,$3,$4,$5,'open')
		ON CONFLICT (order_id, incident_type)
		WHERE status = 'open'
		DO UPDATE SET severity = EXCLUDED.severity,
		              reason = EXCLUDED.reason,
		              payload = EXCLUDED.payload,
		              updated_at = now()`,
		orderID, incidentType, severity, reason, raw)
	if err != nil {
		return fmt.Errorf("OpenOrderIncident: %w", err)
	}
	return db.AddEvent(ctx, orderID, "order.incident."+incidentType, map[string]any{
		"incidentType": incidentType,
		"severity":     severity,
		"reason":       reason,
		"payload":      payload,
	})
}
