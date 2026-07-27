package paymaster

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"payment-gateway/internal/database"
)

// ErrDuplicateSig is returned when an identical EIP-712 (r, s) pair has
// already been processed within the TTL window.
var ErrDuplicateSig = errors.New("paymaster: duplicate signature — relay already submitted")

const sigLockTTL = 2 * time.Minute
const sigLockGCInterval = 30 * time.Second

// SigLockStore is the interface for idempotency stores. Swap in a Redis
// implementation for multi-instance deployments.
type SigLockStore interface {
	// AcquireLock atomically acquires the lock for hash.
	// Returns (true, nil) on first acquisition; (false, ErrDuplicateSig) if already held.
	AcquireLock(hash string) (bool, error)
	// ReleaseLock releases the lock (used only on relay failure to allow retry).
	ReleaseLock(hash string)
}

// PermitSigHash derives the canonical idempotency key from the (r, s) pair of
// an EIP-712 signature. The v component (27 / 28) is intentionally excluded
// to prevent trivial flip-based evasion.
//
// hash = hex(SHA-256(r_bytes || s_bytes))
func PermitSigHash(r, s string) (string, error) {
	rBytes, err := hex.DecodeString(stripHex(r))
	if err != nil {
		return "", err
	}
	sBytes, err := hex.DecodeString(stripHex(s))
	if err != nil {
		return "", err
	}
	combined := append(rBytes, sBytes...) //nolint:gocritic
	sum := sha256.Sum256(combined)
	return hex.EncodeToString(sum[:]), nil
}

func stripHex(s string) string {
	if len(s) >= 2 && s[:2] == "0x" {
		return s[2:]
	}
	return s
}

// ── InMemorySigLock ────────────────────────────────────────────────────────────

type sigEntry struct {
	acquiredAt time.Time
}

// InMemorySigLock is a goroutine-safe in-memory implementation of SigLockStore.
// Suitable for single-instance deployments. For multi-instance, provide a
// Redis-backed implementation via the SigLockStore interface.
type InMemorySigLock struct {
	store sync.Map // hash → *sigEntry
}

// NewInMemorySigLock creates an InMemorySigLock and starts the GC goroutine.
// The GC goroutine exits when ctx is cancelled — pass context.Background() for
// long-lived use.
func NewInMemorySigLock(stopCh <-chan struct{}) *InMemorySigLock {
	sl := &InMemorySigLock{}
	go sl.gc(stopCh)
	return sl
}

func (sl *InMemorySigLock) AcquireLock(hash string) (bool, error) {
	entry := &sigEntry{acquiredAt: time.Now()}
	// LoadOrStore is atomic: only the first caller wins.
	_, loaded := sl.store.LoadOrStore(hash, entry)
	if loaded {
		return false, ErrDuplicateSig
	}
	return true, nil
}

func (sl *InMemorySigLock) ReleaseLock(hash string) {
	sl.store.Delete(hash)
}

func (sl *InMemorySigLock) gc(stopCh <-chan struct{}) {
	ticker := time.NewTicker(sigLockGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			sl.store.Range(func(k, v any) bool {
				e, ok := v.(*sigEntry)
				if !ok || now.Sub(e.acquiredAt) > sigLockTTL {
					sl.store.Delete(k)
				}
				return true
			})
		}
	}
}

// DBSigLock uses PostgreSQL as the primary replay lock so multiple paymaster
// replicas reject the same EIP-712 signature consistently.
type DBSigLock struct {
	db *database.DB
}

func NewDBSigLock(db *database.DB) *DBSigLock {
	return &DBSigLock{db: db}
}

func (sl *DBSigLock) AcquireLock(hash string) (bool, error) {
	if sl == nil || sl.db == nil || sl.db.SQL == nil {
		return false, fmt.Errorf("paymaster: db sig lock unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	tx, err := sl.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `DELETE FROM paymaster_sig_locks WHERE expires_at <= NOW()`); err != nil {
		return false, err
	}

	var acquired string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO paymaster_sig_locks (sig_hash, expires_at)
		VALUES ($1, NOW() + interval '2 minutes')
		ON CONFLICT (sig_hash) DO NOTHING
		RETURNING sig_hash`, hash).Scan(&acquired)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrDuplicateSig
		}
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (sl *DBSigLock) ReleaseLock(hash string) {
	if sl == nil || sl.db == nil || sl.db.SQL == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = sl.db.SQL.ExecContext(ctx, `DELETE FROM paymaster_sig_locks WHERE sig_hash = $1`, hash)
}
