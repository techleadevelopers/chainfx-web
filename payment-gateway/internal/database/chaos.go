package database

import (
	"context"
	"database/sql"
	"time"
)

type AdversarialRun struct {
	ID                int       `json:"id"`
	TriggeredBy       string    `json:"triggered_by"`
	Status            string    `json:"status"`
	ScenariosExecuted int       `json:"scenarios_executed"`
	FailuresDetected  int       `json:"failures_detected"`
	Logs              string    `json:"logs"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (db *DB) EnsureChaosSchema(ctx context.Context) error {
	_, err := db.SQL.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS admin_adversarial_runs (
			id                 SERIAL PRIMARY KEY,
			triggered_by       VARCHAR(255) NOT NULL,
			status             VARCHAR(50)  NOT NULL DEFAULT 'RUNNING',
			scenarios_executed INT          NOT NULL DEFAULT 0,
			failures_detected  INT          NOT NULL DEFAULT 0,
			logs               TEXT,
			created_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
			updated_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_adversarial_runs_created
			ON admin_adversarial_runs (created_at DESC);
	`)
	return err
}

func (db *DB) CreateAdversarialRun(ctx context.Context, triggeredBy string) (int, error) {
	var id int
	err := db.SQL.QueryRowContext(ctx,
		`INSERT INTO admin_adversarial_runs (triggered_by, status)
		 VALUES ($1, 'RUNNING') RETURNING id`,
		triggeredBy,
	).Scan(&id)
	return id, err
}

func (db *DB) CompleteAdversarialRun(ctx context.Context, id, scenarios, failures int, logs, status string) error {
	_, err := db.SQL.ExecContext(ctx,
		`UPDATE admin_adversarial_runs
		 SET status = $2, scenarios_executed = $3, failures_detected = $4, logs = $5, updated_at = NOW()
		 WHERE id = $1`,
		id, status, scenarios, failures, logs,
	)
	return err
}

func (db *DB) ListAdversarialRuns(ctx context.Context, limit int) ([]AdversarialRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT id, triggered_by, status, scenarios_executed, failures_detected, COALESCE(logs,''), created_at, updated_at
		 FROM admin_adversarial_runs
		 ORDER BY created_at DESC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AdversarialRun
	for rows.Next() {
		var r AdversarialRun
		var updatedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.TriggeredBy, &r.Status, &r.ScenariosExecuted, &r.FailuresDetected, &r.Logs, &r.CreatedAt, &updatedAt); err != nil {
			return nil, err
		}
		if updatedAt.Valid {
			r.UpdatedAt = updatedAt.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
