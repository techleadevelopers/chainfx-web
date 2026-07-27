package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

var ErrEIP712NonceReplay = errors.New("EIP-712 nonce already used")

type EIP712NonceInput struct {
	Signer     string
	IntentType string
	Nonce      string
	Digest     string
	ChainID    int64
	ExpiresAt  time.Time
}

func (db *DB) RecordEIP712Nonce(ctx context.Context, input EIP712NonceInput) error {
	if db == nil || db.SQL == nil {
		return nil
	}
	input.Signer = strings.ToLower(strings.TrimSpace(input.Signer))
	input.IntentType = strings.TrimSpace(input.IntentType)
	input.Nonce = strings.ToLower(strings.TrimSpace(input.Nonce))
	input.Digest = strings.ToLower(strings.TrimSpace(input.Digest))
	if input.Signer == "" || input.IntentType == "" || input.Nonce == "" || input.Digest == "" || input.ChainID == 0 {
		return fmt.Errorf("nonce EIP-712 incompleto")
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = time.Now().UTC().Add(15 * time.Minute)
	}
	_, err := db.SQL.ExecContext(ctx, `
INSERT INTO eip712_intent_nonces (signer, intent_type, nonce, digest, chain_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
`, input.Signer, input.IntentType, input.Nonce, input.Digest, input.ChainID, input.ExpiresAt.UTC())
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			return ErrEIP712NonceReplay
		}
		return err
	}
	return nil
}
