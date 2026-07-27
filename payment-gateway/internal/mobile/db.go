package mobile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/models"
)

type mobileWalletKey struct {
	UserID              string
	WalletAddress       string
	EncryptedPrivateKey string
	CustodyMode         string
	Network             string
}

type mobileWalletToken struct {
	ID        string
	UserID    string
	Symbol    string
	Name      string
	Network   string
	Contract  string
	Decimals  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

var errMobileTransferAlreadyRecorded = errors.New("mobile wallet transfer already recorded")

type mobileEmailOTP struct {
	ID         string
	Email      string
	Purpose    string
	CodeHash   string
	Attempts   int
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

type createMobileUserInput struct {
	Email               string
	PasswordHash        string
	FullName            string
	Phone               string
	CPF                 string
	BirthDate           string
	AddressPostalCode   string
	AddressStreet       string
	AddressNumber       string
	AddressNeighborhood string
	AddressCity         string
	AddressState        string
	AddressCountry      string
}

// mobileDB wraps the existing DB to add mobile-specific queries.
// Uses DB.SQL directly so we don't touch the existing database package.
type mobileQueries struct {
	sql *sql.DB
}

func mobileDB(db *database.DB) *mobileQueries {
	return &mobileQueries{sql: db.SQL}
}

// ─── Users ────────────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateUser(ctx context.Context, input createMobileUserInput) (*models.User, error) {
	if err := q.ensureMobileMediaSchema(ctx); err != nil {
		return nil, err
	}
	email := strings.TrimSpace(strings.ToLower(input.Email))
	var id string
	err := q.sql.QueryRowContext(ctx, `
                INSERT INTO users
                  (email, password_hash, full_name, phone, cpf, birth_date,
                   address_postal_code, address_street, address_number,
                   address_neighborhood, address_city, address_state, address_country)
                VALUES
                  ($1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NULLIF($6,''),
                   NULLIF($7,''), NULLIF($8,''), NULLIF($9,''),
                   NULLIF($10,''), NULLIF($11,''), NULLIF($12,''), NULLIF($13,''))
                RETURNING id`,
		email, input.PasswordHash, input.FullName, input.Phone, input.CPF, input.BirthDate,
		input.AddressPostalCode, input.AddressStreet, input.AddressNumber,
		input.AddressNeighborhood, input.AddressCity, input.AddressState, firstNonEmptyStr(input.AddressCountry, "BR")).Scan(&id)
	if err != nil {
		return nil, err
	}
	return q.GetUserByID(ctx, id)
}

func (q *mobileQueries) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	return q.scanUser(q.sql.QueryRowContext(ctx, `
                SELECT id,email,phone,full_name,cpf,birth_date,
                       address_postal_code,address_street,address_number,address_neighborhood,address_city,address_state,address_country,
                       avatar_url,password_hash,wallet_address,pix_key,
                       kyc_status,kyc_documents,pin_hash,biometry_enabled,two_factor_enabled,
                       two_factor_secret,refresh_token_hash,created_at,updated_at
                FROM users WHERE lower(email)=lower($1) AND deleted_at IS NULL`, email))
}

func (q *mobileQueries) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	return q.scanUser(q.sql.QueryRowContext(ctx, `
                SELECT id,email,phone,full_name,cpf,birth_date,
                       address_postal_code,address_street,address_number,address_neighborhood,address_city,address_state,address_country,
                       avatar_url,password_hash,wallet_address,pix_key,
                       kyc_status,kyc_documents,pin_hash,biometry_enabled,two_factor_enabled,
                       two_factor_secret,refresh_token_hash,created_at,updated_at
                FROM users WHERE id=$1 AND deleted_at IS NULL`, id))
}

// GetUserWalletAddress fetches only the wallet_address for the given user — used
// by NFC handlers to avoid loading the full user row just for one field.
func (q *mobileQueries) GetUserWalletAddress(ctx context.Context, userID string) (string, error) {
	var addr *string
	err := q.sql.QueryRowContext(ctx,
		`SELECT wallet_address FROM users WHERE id=$1 AND deleted_at IS NULL`, userID).Scan(&addr)
	if err != nil {
		return "", err
	}
	if addr == nil {
		return "", nil
	}
	return *addr, nil
}

// GetUserWalletAndName fetches wallet_address and full_name in a single query.
// Used by NFC card handlers to avoid a full user row fetch and a separate name lookup.
func (q *mobileQueries) GetUserWalletAndName(ctx context.Context, userID string) (addr, fullName string, err error) {
	var addrPtr, namePtr *string
	err = q.sql.QueryRowContext(ctx,
		`SELECT wallet_address, full_name FROM users WHERE id=$1 AND deleted_at IS NULL`, userID).
		Scan(&addrPtr, &namePtr)
	if err != nil {
		return "", "", err
	}
	if addrPtr != nil {
		addr = *addrPtr
	}
	if namePtr != nil {
		fullName = *namePtr
	}
	return addr, fullName, nil
}

// InitSchema runs all one-time DDL migrations for the mobile package.
// Call this once at server startup instead of on every query.
func (q *mobileQueries) InitSchema(ctx context.Context) error {
	if err := q.ensureMobileMediaSchema(ctx); err != nil {
		return err
	}
	if err := q.ensureMobileDCASchema(ctx); err != nil {
		return err
	}
	if err := q.ensureMobileWalletKeySchema(ctx); err != nil {
		return err
	}
	if err := q.ensureMobileWalletTransferSchema(ctx); err != nil {
		return err
	}
	if err := q.ensureMobileWalletTokenSchema(ctx); err != nil {
		return err
	}
	if err := q.ensureMobilePaySchema(ctx); err != nil {
		return err
	}
	if err := q.ensureMobileGiftCardSchema(ctx); err != nil {
		return err
	}
	return q.ensureMobileEmailOTPSchema(ctx)
}

func (q *mobileQueries) scanUser(row *sql.Row) (*models.User, error) {
	u := &models.User{}
	err := row.Scan(
		&u.ID, &u.Email, &u.Phone, &u.FullName, &u.CPF, &u.BirthDate,
		&u.AddressPostalCode, &u.AddressStreet, &u.AddressNumber, &u.AddressNeighborhood, &u.AddressCity, &u.AddressState, &u.AddressCountry,
		&u.AvatarURL,
		&u.PasswordHash, &u.WalletAddress, &u.PixKey, &u.KYCStatus, &u.KYCDocuments,
		&u.PinHash, &u.BiometryEnabled, &u.TwoFactorEnabled,
		&u.TwoFactorSecret, &u.RefreshTokenHash,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (q *mobileQueries) ensureMobileMediaSchema(ctx context.Context) error {
	if _, err := q.sql.ExecContext(ctx, `ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url VARCHAR(2048)`); err != nil {
		return err
	}
	for _, stmt := range []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS birth_date TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS cpf TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS address_postal_code TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS address_street TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS address_number TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS address_neighborhood TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS address_city TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS address_state TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS address_country TEXT`,
	} {
		if _, err := q.sql.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if _, err := q.sql.ExecContext(ctx, `ALTER TABLE kyc_requests ADD COLUMN IF NOT EXISTS document_back_url VARCHAR(2048)`); err != nil {
		return err
	}
	_, err := q.sql.ExecContext(ctx, `ALTER TABLE kyc_requests ADD COLUMN IF NOT EXISTS facial_video_url VARCHAR(2048)`)
	return err
}

func (q *mobileQueries) ensureMobileDCASchema(ctx context.Context) error {
	_, err := q.sql.ExecContext(ctx, `
                ALTER TABLE dca_strategies
                  ADD COLUMN IF NOT EXISTS network TEXT NOT NULL DEFAULT 'BSC'`)
	if err != nil {
		return err
	}
	_, err = q.sql.ExecContext(ctx, `
                CREATE INDEX IF NOT EXISTS idx_dca_user_network
                  ON dca_strategies (user_id, token_symbol, network)`)
	return err
}

func (q *mobileQueries) ensureMobileEmailOTPSchema(ctx context.Context) error {
	_, err := q.sql.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS mobile_email_otps (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email TEXT NOT NULL,
  purpose TEXT NOT NULL,
  code_hash TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_mobile_email_otps_lookup
  ON mobile_email_otps (lower(email), purpose, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mobile_email_otps_active
  ON mobile_email_otps (lower(email), purpose, expires_at)
  WHERE consumed_at IS NULL;`)
	return err
}

func (q *mobileQueries) CreateEmailOTP(ctx context.Context, email, purpose, codeHash string, expiresAt time.Time) error {
	if err := q.ensureMobileEmailOTPSchema(ctx); err != nil {
		return err
	}
	_, err := q.sql.ExecContext(ctx, `
INSERT INTO mobile_email_otps (email, purpose, code_hash, expires_at)
VALUES (lower($1), $2, $3, $4)`, strings.TrimSpace(strings.ToLower(email)), purpose, codeHash, expiresAt)
	return err
}

func (q *mobileQueries) LatestEmailOTP(ctx context.Context, email, purpose string) (*mobileEmailOTP, error) {
	if err := q.ensureMobileEmailOTPSchema(ctx); err != nil {
		return nil, err
	}
	row := q.sql.QueryRowContext(ctx, `
SELECT id::text, email, purpose, code_hash, attempts, expires_at, consumed_at, created_at
FROM mobile_email_otps
WHERE lower(email)=lower($1) AND purpose=$2
ORDER BY created_at DESC
LIMIT 1`, strings.TrimSpace(strings.ToLower(email)), purpose)
	var otp mobileEmailOTP
	err := row.Scan(&otp.ID, &otp.Email, &otp.Purpose, &otp.CodeHash, &otp.Attempts, &otp.ExpiresAt, &otp.ConsumedAt, &otp.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &otp, nil
}

func (q *mobileQueries) ConsumeEmailOTP(ctx context.Context, id string) error {
	if err := q.ensureMobileEmailOTPSchema(ctx); err != nil {
		return err
	}
	_, err := q.sql.ExecContext(ctx, `
UPDATE mobile_email_otps
SET consumed_at=NOW()
WHERE id=$1::uuid AND consumed_at IS NULL`, id)
	return err
}

func (q *mobileQueries) IncrementEmailOTPAttempts(ctx context.Context, id string) error {
	if err := q.ensureMobileEmailOTPSchema(ctx); err != nil {
		return err
	}
	_, err := q.sql.ExecContext(ctx, `
UPDATE mobile_email_otps
SET attempts=attempts+1
WHERE id=$1::uuid`, id)
	return err
}

func (q *mobileQueries) UpdateUser(ctx context.Context, id string, fields map[string]any) error {
	set := ""
	args := []any{}
	i := 1
	for k, v := range fields {
		if set != "" {
			set += ", "
		}
		set += k + "=$" + itoa(i)
		args = append(args, v)
		i++
	}
	if set != "" {
		set += ", "
	}
	set += "updated_at=NOW()"
	args = append(args, id)
	_, err := q.sql.ExecContext(ctx, "UPDATE users SET "+set+" WHERE id=$"+itoa(i), args...)
	return err
}

func (q *mobileQueries) AttachSystemWallet(ctx context.Context, userID, walletAddress, encryptedPrivateKey string) (*models.User, error) {
	if err := q.ensureMobileWalletKeySchema(ctx); err != nil {
		return nil, err
	}

	tx, err := q.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
                INSERT INTO mobile_wallet_keys (user_id, wallet_address, encrypted_private_key)
                VALUES ($1::uuid, $2, $3)
                ON CONFLICT (user_id) DO NOTHING`,
		userID, walletAddress, encryptedPrivateKey)
	if err != nil {
		return nil, err
	}

	var selectedAddress string
	err = tx.QueryRowContext(ctx, `
                SELECT wallet_address
                  FROM mobile_wallet_keys
                 WHERE user_id=$1::uuid`, userID).Scan(&selectedAddress)
	if err != nil {
		return nil, err
	}

	_, err = tx.ExecContext(ctx, `
                UPDATE users
                   SET wallet_address=$1,
                       updated_at=NOW()
                 WHERE id=$2::uuid
                   AND deleted_at IS NULL
                   AND (wallet_address IS NULL OR wallet_address='')`,
		selectedAddress, userID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return q.GetUserByID(ctx, userID)
}

func (q *mobileQueries) UpsertCustodialWalletKey(ctx context.Context, userID, walletAddress, encryptedPrivateKey string) error {
	if err := q.ensureMobileWalletKeySchema(ctx); err != nil {
		return err
	}
	_, err := q.sql.ExecContext(ctx, `
                INSERT INTO mobile_wallet_keys (user_id, wallet_address, encrypted_private_key)
                VALUES ($1::uuid, $2, $3)
                ON CONFLICT (user_id) DO UPDATE SET
                  wallet_address=EXCLUDED.wallet_address,
                  encrypted_private_key=EXCLUDED.encrypted_private_key,
                  custody_mode='system_custody',
                  network='EVM',
                  updated_at=NOW()`,
		userID, walletAddress, encryptedPrivateKey)
	return err
}

func (q *mobileQueries) GetCustodialWalletKey(ctx context.Context, userID, walletAddress string) (*mobileWalletKey, error) {
	if err := q.ensureMobileWalletKeySchema(ctx); err != nil {
		return nil, err
	}
	k := &mobileWalletKey{}
	err := q.sql.QueryRowContext(ctx, `
                SELECT user_id::text, wallet_address, encrypted_private_key, custody_mode, network
                  FROM mobile_wallet_keys
                 WHERE user_id=$1::uuid
                   AND lower(wallet_address)=lower($2)`,
		userID, walletAddress).Scan(&k.UserID, &k.WalletAddress, &k.EncryptedPrivateKey, &k.CustodyMode, &k.Network)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return k, err
}

func (q *mobileQueries) RecordMobileWalletTransfer(ctx context.Context, userID, from, to, token, asset, network, amount, amountRaw, txHash, idempotencyKey string) error {
	if err := q.ensureMobileWalletTransferSchema(ctx); err != nil {
		return err
	}
	res, err := q.sql.ExecContext(ctx, `
                INSERT INTO mobile_wallet_transfers
                  (user_id, from_address, to_address, token_contract, asset, network, amount, amount_raw, tx_hash, idempotency_key)
                VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10,''))
                ON CONFLICT (user_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING`,
		userID, from, to, token, asset, network, amount, amountRaw, txHash, idempotencyKey)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return errMobileTransferAlreadyRecorded
	}
	return nil
}

func (q *mobileQueries) UpsertWalletToken(ctx context.Context, userID, network, contract, symbol, name string, decimals int) (*mobileWalletToken, error) {
	if err := q.ensureMobileWalletTokenSchema(ctx); err != nil {
		return nil, err
	}
	if decimals <= 0 {
		decimals = 18
	}
	token := &mobileWalletToken{}
	err := q.sql.QueryRowContext(ctx, `
                INSERT INTO mobile_wallet_tokens
                  (user_id, network, contract_address, symbol, name, decimals)
                VALUES ($1::uuid, $2, $3, $4, $5, $6)
                ON CONFLICT (user_id, network, symbol, contract_address) DO UPDATE SET
                  name=EXCLUDED.name,
                  decimals=EXCLUDED.decimals,
                  updated_at=NOW()
                RETURNING id::text, user_id::text, symbol, name, network, contract_address, decimals, created_at, updated_at`,
		userID, network, contract, symbol, name, decimals).Scan(
		&token.ID, &token.UserID, &token.Symbol, &token.Name, &token.Network, &token.Contract, &token.Decimals, &token.CreatedAt, &token.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return token, nil
}

func (q *mobileQueries) ListWalletTokens(ctx context.Context, userID string) ([]mobileWalletToken, error) {
	if err := q.ensureMobileWalletTokenSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id::text, user_id::text, symbol, name, network, contract_address, decimals, created_at, updated_at
                  FROM mobile_wallet_tokens
                 WHERE user_id=$1::uuid
                 ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mobileWalletToken
	for rows.Next() {
		token := mobileWalletToken{}
		if err := rows.Scan(&token.ID, &token.UserID, &token.Symbol, &token.Name, &token.Network, &token.Contract, &token.Decimals, &token.CreatedAt, &token.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, token)
	}
	return out, rows.Err()
}

func (q *mobileQueries) ensureMobileWalletKeySchema(ctx context.Context) error {
	_, err := q.sql.ExecContext(ctx, `
                CREATE TABLE IF NOT EXISTS mobile_wallet_keys (
                  user_id               UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
                  wallet_address        TEXT        NOT NULL UNIQUE,
                  encrypted_private_key TEXT        NOT NULL,
                  custody_mode          TEXT        NOT NULL DEFAULT 'system_custody',
                  network               TEXT        NOT NULL DEFAULT 'EVM',
                  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
                  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
                )`)
	if err != nil {
		return err
	}
	_, err = q.sql.ExecContext(ctx, `
                CREATE INDEX IF NOT EXISTS idx_mobile_wallet_keys_address
                  ON mobile_wallet_keys (lower(wallet_address))`)
	return err
}

func (q *mobileQueries) ensureMobileWalletTransferSchema(ctx context.Context) error {
	_, err := q.sql.ExecContext(ctx, `
                CREATE TABLE IF NOT EXISTS mobile_wallet_transfers (
                  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
                  user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                  from_address    TEXT        NOT NULL,
                  to_address      TEXT        NOT NULL,
                  token_contract  TEXT        NOT NULL,
                  asset           TEXT        NOT NULL,
                  network         TEXT        NOT NULL,
                  amount          TEXT        NOT NULL,
                  amount_raw      TEXT        NOT NULL,
                  tx_hash         TEXT        NOT NULL UNIQUE,
                  idempotency_key TEXT,
                  status          TEXT        NOT NULL DEFAULT 'submitted',
                  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
                )`)
	if err != nil {
		return err
	}
	_, err = q.sql.ExecContext(ctx, `
                CREATE INDEX IF NOT EXISTS idx_mobile_wallet_transfers_user_created
                  ON mobile_wallet_transfers (user_id, created_at DESC)`)
	if err != nil {
		return err
	}
	_, err = q.sql.ExecContext(ctx, `
                CREATE UNIQUE INDEX IF NOT EXISTS uq_mobile_wallet_transfers_user_idempotency
                  ON mobile_wallet_transfers (user_id, idempotency_key)
                  WHERE idempotency_key IS NOT NULL`)
	return err
}

func (q *mobileQueries) ensureMobileWalletTokenSchema(ctx context.Context) error {
	_, err := q.sql.ExecContext(ctx, `
                CREATE TABLE IF NOT EXISTS mobile_wallet_tokens (
                  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
                  user_id          UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                  network          TEXT        NOT NULL,
                  contract_address TEXT        NOT NULL DEFAULT '',
                  symbol           TEXT        NOT NULL,
                  name             TEXT        NOT NULL,
                  decimals         INTEGER     NOT NULL DEFAULT 18,
                  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
                  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
                  UNIQUE (user_id, network, symbol, contract_address)
                )`)
	if err != nil {
		return err
	}
	_, err = q.sql.ExecContext(ctx, `
                CREATE INDEX IF NOT EXISTS idx_mobile_wallet_tokens_user
                  ON mobile_wallet_tokens (user_id, created_at DESC)`)
	return err
}

func (q *mobileQueries) ensureMobilePaySchema(ctx context.Context) error {
	_, err := q.sql.ExecContext(ctx, `
                CREATE TABLE IF NOT EXISTS mobile_payment_intents (
                  id                  TEXT        PRIMARY KEY,
                  user_id             UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                  wallet_address      TEXT        NOT NULL,
                  idempotency_key     TEXT        NOT NULL,
                  raw_code            TEXT        NOT NULL,
                  payment_type        TEXT        NOT NULL,
                  beneficiary_name    TEXT        NOT NULL DEFAULT '',
                  document            TEXT        NOT NULL DEFAULT '',
                  description         TEXT        NOT NULL DEFAULT '',
                  amount_brl          NUMERIC(18,2) NOT NULL,
                  fee_brl             NUMERIC(18,2) NOT NULL DEFAULT 0,
                  usdt_rate           NUMERIC(20,8) NOT NULL,
                  required_usdt_micro BIGINT      NOT NULL,
                  status              TEXT        NOT NULL DEFAULT 'processing',
                  provider            TEXT        NOT NULL DEFAULT 'efi',
                  provider_status     TEXT        NOT NULL DEFAULT '',
                  provider_reference  TEXT        NOT NULL DEFAULT '',
                  error_message       TEXT        NOT NULL DEFAULT '',
                  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
                  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
                  UNIQUE (user_id, idempotency_key)
                )`)
	if err != nil {
		return err
	}
	_, err = q.sql.ExecContext(ctx, `
                CREATE INDEX IF NOT EXISTS idx_mobile_payment_intents_user_created
                  ON mobile_payment_intents (user_id, created_at DESC)`)
	return err
}

func (q *mobileQueries) ensureMobileGiftCardSchema(ctx context.Context) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS gift_card_providers (
                  id               TEXT        PRIMARY KEY,
                  slug             TEXT        NOT NULL UNIQUE,
                  name             TEXT        NOT NULL,
                  status           TEXT        NOT NULL DEFAULT 'active',
                  api_base_url     TEXT        NOT NULL DEFAULT '',
                  settlement_asset TEXT        NOT NULL DEFAULT 'USDT',
                  settlement_network TEXT      NOT NULL DEFAULT 'BSC',
                  metadata         JSONB       NOT NULL DEFAULT '{}'::jsonb,
                  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
                  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE TABLE IF NOT EXISTS gift_card_provider_products (
                  id                 TEXT        PRIMARY KEY,
                  provider_id        TEXT        NOT NULL REFERENCES gift_card_providers(id),
                  product_id         TEXT        NOT NULL UNIQUE,
                  external_sku       TEXT        NOT NULL DEFAULT '',
                  brand              TEXT        NOT NULL,
                  title              TEXT        NOT NULL,
                  description        TEXT        NOT NULL DEFAULT '',
                  category           TEXT        NOT NULL DEFAULT 'gift_cards',
                  currency           TEXT        NOT NULL DEFAULT 'BRL',
                  face_value_minor   BIGINT      NOT NULL,
                  price_brl          NUMERIC(18,2) NOT NULL,
                  discount_bps       INTEGER     NOT NULL DEFAULT 0,
                  product_type       TEXT        NOT NULL DEFAULT 'gift_card',
                  delivery_mode      TEXT        NOT NULL DEFAULT 'code',
                  image_url          TEXT        NOT NULL DEFAULT '',
                  active             BOOLEAN     NOT NULL DEFAULT true,
                  requires_kyc       BOOLEAN     NOT NULL DEFAULT true,
                  metadata           JSONB       NOT NULL DEFAULT '{}'::jsonb,
                  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
                  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE TABLE IF NOT EXISTS mobile_gift_cards (
                  id                  TEXT        PRIMARY KEY,
                  provider_product_id TEXT        NOT NULL REFERENCES gift_card_provider_products(id),
                  brand               TEXT        NOT NULL,
                  title               TEXT        NOT NULL,
                  subtitle            TEXT        NOT NULL DEFAULT '',
                  badge               TEXT        NOT NULL DEFAULT '',
                  offer_text          TEXT        NOT NULL DEFAULT '',
                  sort_order          INTEGER     NOT NULL DEFAULT 100,
                  active              BOOLEAN     NOT NULL DEFAULT true,
                  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
                  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE TABLE IF NOT EXISTS mobile_gift_card_orders (
                  id                  TEXT        PRIMARY KEY,
                  user_id             UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                  wallet_address      TEXT        NOT NULL,
                  idempotency_key     TEXT        NOT NULL,
                  product_id          TEXT        NOT NULL REFERENCES gift_card_provider_products(product_id),
                  provider_id         TEXT        NOT NULL REFERENCES gift_card_providers(id),
                  provider_product_id TEXT        NOT NULL REFERENCES gift_card_provider_products(id),
                  quantity            INTEGER     NOT NULL DEFAULT 1,
                  amount_brl          NUMERIC(18,2) NOT NULL,
                  fee_brl             NUMERIC(18,2) NOT NULL DEFAULT 0,
                  usdt_rate           NUMERIC(20,8) NOT NULL,
                  required_usdt_micro BIGINT      NOT NULL,
                  status              TEXT        NOT NULL DEFAULT 'processing',
                  provider_status     TEXT        NOT NULL DEFAULT '',
                  provider_reference  TEXT        NOT NULL DEFAULT '',
                  redemption_code     TEXT        NOT NULL DEFAULT '',
                  redemption_pin      TEXT        NOT NULL DEFAULT '',
                  redemption_url      TEXT        NOT NULL DEFAULT '',
                  email_status        TEXT        NOT NULL DEFAULT '',
                  error_message       TEXT        NOT NULL DEFAULT '',
                  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
                  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
                  UNIQUE (user_id, idempotency_key)
                )`,
		`CREATE TABLE IF NOT EXISTS gift_card_quotes (
                  id                  TEXT        PRIMARY KEY,
                  user_id             UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                  product_id          TEXT        NOT NULL REFERENCES gift_card_provider_products(product_id),
                  quantity            INTEGER     NOT NULL DEFAULT 1,
                  funding_method      TEXT        NOT NULL DEFAULT 'internal_usdt',
                  amount_brl          NUMERIC(18,2) NOT NULL,
                  fee_brl             NUMERIC(18,2) NOT NULL DEFAULT 0,
                  total_brl           NUMERIC(18,2) NOT NULL,
                  usdt_rate           NUMERIC(20,8) NOT NULL,
                  required_usdt_micro BIGINT      NOT NULL,
                  expires_at          TIMESTAMPTZ NOT NULL,
                  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE TABLE IF NOT EXISTS gift_card_deliveries (
                  id                   TEXT        PRIMARY KEY,
                  order_id             TEXT        NOT NULL REFERENCES mobile_gift_card_orders(id) ON DELETE CASCADE,
                  redemption_type      TEXT        NOT NULL DEFAULT 'code',
                  redemption_code_enc  TEXT        NOT NULL DEFAULT '',
                  redemption_pin_enc   TEXT        NOT NULL DEFAULT '',
                  redemption_url_enc   TEXT        NOT NULL DEFAULT '',
                  expires_at           TIMESTAMPTZ,
                  delivered_at         TIMESTAMPTZ,
                  created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE TABLE IF NOT EXISTS gift_card_provider_attempts (
                  id                  TEXT        PRIMARY KEY,
                  order_id            TEXT        NOT NULL REFERENCES mobile_gift_card_orders(id) ON DELETE CASCADE,
                  provider_id         TEXT        NOT NULL REFERENCES gift_card_providers(id),
                  action              TEXT        NOT NULL,
                  status              TEXT        NOT NULL,
                  provider_reference  TEXT        NOT NULL DEFAULT '',
                  error_message       TEXT        NOT NULL DEFAULT '',
                  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE TABLE IF NOT EXISTS gift_card_webhook_events (
                  id                  TEXT        PRIMARY KEY,
                  provider            TEXT        NOT NULL,
                  event_type          TEXT        NOT NULL,
                  external_id         TEXT        NOT NULL DEFAULT '',
                  payload             JSONB       NOT NULL DEFAULT '{}'::jsonb,
                  processed_at        TIMESTAMPTZ,
                  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE TABLE IF NOT EXISTS commerce_outbox_events (
                  id                  TEXT        PRIMARY KEY,
                  event_type          TEXT        NOT NULL,
                  aggregate_id        TEXT        NOT NULL,
                  provider            TEXT        NOT NULL DEFAULT '',
                  payload             JSONB       NOT NULL DEFAULT '{}'::jsonb,
                  status              TEXT        NOT NULL DEFAULT 'pending',
                  attempts            INTEGER     NOT NULL DEFAULT 0,
                  next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
                  last_error          TEXT        NOT NULL DEFAULT '',
                  locked_at           TIMESTAMPTZ,
                  processed_at        TIMESTAMPTZ,
                  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
                  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE TABLE IF NOT EXISTS mobile_wallet_ledger_entries (
                  id                  TEXT        PRIMARY KEY,
                  wallet_address      TEXT        NOT NULL,
                  network             TEXT        NOT NULL DEFAULT 'BSC',
                  asset               TEXT        NOT NULL DEFAULT 'USDT',
                  source              TEXT        NOT NULL,
                  reference_id        TEXT        NOT NULL,
                  available_delta_micro BIGINT    NOT NULL DEFAULT 0,
                  locked_delta_micro  BIGINT      NOT NULL DEFAULT 0,
                  metadata            JSONB       NOT NULL DEFAULT '{}'::jsonb,
                  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
                )`,
		`CREATE INDEX IF NOT EXISTS idx_gift_card_provider_products_active
                  ON gift_card_provider_products (active, category, brand)`,
		`CREATE INDEX IF NOT EXISTS idx_mobile_gift_cards_active
                  ON mobile_gift_cards (active, sort_order)`,
		`CREATE INDEX IF NOT EXISTS idx_mobile_gift_card_orders_user_created
                  ON mobile_gift_card_orders (user_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_commerce_outbox_pending
                  ON commerce_outbox_events (status, next_attempt_at, created_at)`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS funding_method TEXT NOT NULL DEFAULT 'internal_usdt'`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS pix_code TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS pix_expires_at TIMESTAMPTZ`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS captured_at TIMESTAMPTZ`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS refunded_at TIMESTAMPTZ`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS redemption_code_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS redemption_pin_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS redemption_url_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS provider_order_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE mobile_gift_card_orders ADD COLUMN IF NOT EXISTS recipient_phone TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := q.sql.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}

	for _, stmt := range []string{
		`INSERT INTO gift_card_providers (id, slug, name, status)
                 VALUES
                   ('provider_ifood', 'ifood', 'iFood Provider', 'active'),
                   ('provider_openai', 'openai', 'OpenAI Credits Provider', 'active'),
                   ('provider_cloud', 'cloud', 'Cloud Credits Provider', 'active'),
                   ('provider_crypto_voucher', 'crypto_voucher', 'Crypto Voucher Provider', 'active'),
                   ('provider_bitrefill', 'bitrefill', 'Bitrefill', 'active')
                 ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO gift_card_provider_products
                   (id, provider_id, product_id, external_sku, brand, title, description, category,
                    currency, face_value_minor, price_brl, discount_bps, product_type, delivery_mode, image_url, active, requires_kyc)
                 VALUES
                   ('gcpp_ifood_50', 'provider_ifood', 'ifood-br-50', 'IFOOD-BR-50', 'iFood', 'Ifood Gift Cards', 'Gift card iFood pago com USDT', 'food', 'BRL', 5000, 50.00, 500, 'gift_card', 'code', '', true, true),
                   ('gcpp_ifood_100', 'provider_ifood', 'ifood-br-100', 'IFOOD-BR-100', 'iFood', 'Ifood Gift Cards', 'Gift card iFood pago com USDT', 'food', 'BRL', 10000, 100.00, 500, 'gift_card', 'code', '', true, true),
                   ('gcpp_openai_100', 'provider_openai', 'openai-br-100', 'OPENAI-BR-100', 'OpenAI', 'AI Credits', 'Creditos de IA pagos com USDT', 'ai', 'BRL', 10000, 100.00, 1000, 'gift_card', 'link', '', true, true),
                   ('gcpp_cloud_100', 'provider_cloud', 'cloud-br-100', 'CLOUD-BR-100', 'Cloud Gift', 'Cloud Gift', 'Creditos de infraestrutura pagos com USDT', 'cloud', 'BRL', 10000, 100.00, 0, 'gift_card', 'code', '', true, true),
                   ('gcpp_usdt_voucher_25', 'provider_crypto_voucher', 'usdt-voucher-br-25', 'USDT-VOUCHER-BR-25', 'USDT', 'USDT Voucher', 'Voucher cripto resgatavel sujeito a KYC e limites', 'crypto_voucher', 'BRL', 2500, 25.00, 0, 'crypto_voucher', 'voucher', '', true, true),
                   ('gcpp_btc_voucher_25', 'provider_crypto_voucher', 'btc-voucher-br-25', 'BTC-VOUCHER-BR-25', 'BTC', 'BTC Voucher', 'Voucher cripto resgatavel sujeito a KYC e limites', 'crypto_voucher', 'BRL', 2500, 25.00, 0, 'crypto_voucher', 'voucher', '', true, true)
                 ON CONFLICT (product_id) DO NOTHING`,
		`INSERT INTO mobile_gift_cards
                   (id, provider_product_id, brand, title, subtitle, badge, offer_text, sort_order, active)
                 VALUES
                   ('mgc_ifood_50', 'gcpp_ifood_50', 'iFood', 'Ifood Gift Cards', 'Pague com cripto', 'Em alta', 'Ate 50% de desconto', 10, true),
                   ('mgc_openai_100', 'gcpp_openai_100', 'OpenAI', 'AI Credits', 'OpenAI', 'Em alta', '10% de desconto', 20, true),
                   ('mgc_cloud_100', 'gcpp_cloud_100', 'Cloud Gift', 'Cloud Gift', 'Infra credits', 'Novo', 'Cashback azul', 30, true),
                   ('mgc_usdt_voucher_25', 'gcpp_usdt_voucher_25', 'USDT', 'USDT Voucher', 'Voucher cripto', 'KYC', 'Resgate em USDT', 40, true),
                   ('mgc_btc_voucher_25', 'gcpp_btc_voucher_25', 'BTC', 'BTC Voucher', 'Voucher cripto', 'KYC', 'Resgate em BTC', 50, true)
                 ON CONFLICT (id) DO NOTHING`,
	} {
		if _, err := q.sql.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (q *mobileQueries) IsUserActive(ctx context.Context, userID string) (bool, error) {
	var exists bool
	err := q.sql.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND deleted_at IS NULL)", userID).Scan(&exists)
	return exists, err
}

func (q *mobileQueries) DeleteUserAccount(ctx context.Context, userID string) error {
	tx, err := q.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM devices WHERE user_id=$1", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM notifications WHERE user_id=$1", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE user_id=$1", userID); err != nil {
		return err
	}
	if err := execIfTableExists(ctx, tx, "dca_strategies", "UPDATE dca_strategies SET active=false WHERE user_id=$1", userID); err != nil {
		return err
	}
	if err := execIfTableExists(ctx, tx, "webhook_subscriptions", "UPDATE webhook_subscriptions SET active=false, updated_at=NOW() WHERE user_id=$1::uuid", userID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
                UPDATE users
                   SET email='deleted+' || id::text || '@deleted.chainfx.local',
                       phone=NULL,
                       full_name=NULL,
                       password_hash='deleted',
                       wallet_address=NULL,
                       pix_key=NULL,
                       avatar_url=NULL,
                       kyc_documents=NULL,
                       pin_hash=NULL,
                       biometry_enabled=false,
                       two_factor_enabled=false,
                       two_factor_secret=NULL,
                       refresh_token_hash=NULL,
                       deleted_at=NOW(),
                       updated_at=NOW()
                 WHERE id=$1 AND deleted_at IS NULL`, userID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func execIfTableExists(ctx context.Context, tx *sql.Tx, tableName, query string, args ...any) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", tableName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func (q *mobileQueries) SaveRefreshToken(ctx context.Context, userID, token string) error {
	hash := refreshTokenDigest(token)
	_, err := q.sql.ExecContext(ctx,
		"UPDATE users SET refresh_token_hash=$1 WHERE id=$2", hash, userID)
	return err
}

func refreshTokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (q *mobileQueries) ClearRefreshToken(ctx context.Context, userID string) error {
	_, err := q.sql.ExecContext(ctx,
		"UPDATE users SET refresh_token_hash=NULL WHERE id=$1", userID)
	return err
}

// ─── Devices ──────────────────────────────────────────────────────────────────

func (q *mobileQueries) UpsertDevice(ctx context.Context, userID, deviceName, deviceType, fcmToken, apnsToken string) error {
	_, err := q.sql.ExecContext(ctx, `
                INSERT INTO devices (user_id, device_name, device_type, fcm_token, apns_token, last_active)
                VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NOW())
                ON CONFLICT (user_id, COALESCE(fcm_token,''), COALESCE(apns_token,''))
                DO UPDATE SET last_active=NOW(), device_name=EXCLUDED.device_name, device_type=EXCLUDED.device_type`,
		userID, deviceName, deviceType, fcmToken, apnsToken)
	if err != nil {
		// fallback: plain insert ignoring conflict
		_, err = q.sql.ExecContext(ctx, `
                        INSERT INTO devices (user_id, device_name, device_type, fcm_token, apns_token, last_active)
                        VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NOW())
                        ON CONFLICT DO NOTHING`,
			userID, deviceName, deviceType, fcmToken, apnsToken)
	}
	return err
}

func (q *mobileQueries) ListDevices(ctx context.Context, userID string) ([]models.Device, error) {
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id,user_id,device_name,device_type,fcm_token,apns_token,last_active,created_at
                FROM devices WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Device
	for rows.Next() {
		d := models.Device{}
		_ = rows.Scan(&d.ID, &d.UserID, &d.DeviceName, &d.DeviceType, &d.FCMToken, &d.APNSToken, &d.LastActive, &d.CreatedAt)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (q *mobileQueries) DeleteDevice(ctx context.Context, userID, deviceID string) error {
	_, err := q.sql.ExecContext(ctx, "DELETE FROM devices WHERE id=$1 AND user_id=$2", deviceID, userID)
	return err
}

// ─── DCA ─────────────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateDCA(ctx context.Context, userID, symbol, network string, amount float64, freq models.DCAFrequency) (*models.DCAStrategy, error) {
	next := nextDCAExecution(freq)
	d := &models.DCAStrategy{}
	err := q.sql.QueryRowContext(ctx, `
                INSERT INTO dca_strategies (user_id, token_symbol, network, amount_brl, frequency, next_execution)
                VALUES ($1,$2,$3,$4,$5,$6)
                RETURNING id,user_id,token_symbol,network,amount_brl,frequency,active,
                          total_invested,total_tokens,next_execution,created_at`,
		userID, symbol, network, amount, string(freq), next).Scan(
		&d.ID, &d.UserID, &d.TokenSymbol, &d.Network, &d.AmountBRL, &d.Frequency,
		&d.Active, &d.TotalInvested, &d.TotalTokens, &d.NextExecution, &d.CreatedAt)
	return d, err
}

func (q *mobileQueries) GetDCA(ctx context.Context, id string) (*models.DCAStrategy, error) {
	row := q.sql.QueryRowContext(ctx, `
                SELECT id,user_id,token_symbol,network,amount_brl,frequency,active,
                       total_invested,total_tokens,next_execution,created_at
                FROM dca_strategies WHERE id=$1`, id)
	return q.scanDCA(row)
}

func (q *mobileQueries) GetDCAByUser(ctx context.Context, id, userID string) (*models.DCAStrategy, error) {
	row := q.sql.QueryRowContext(ctx, `
                SELECT id,user_id,token_symbol,network,amount_brl,frequency,active,
                       total_invested,total_tokens,next_execution,created_at
                FROM dca_strategies WHERE id=$1 AND user_id=$2`, id, userID)
	return q.scanDCA(row)
}

func (q *mobileQueries) ListDCA(ctx context.Context, userID string) ([]models.DCAStrategy, error) {
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id,user_id,token_symbol,network,amount_brl,frequency,active,
                       total_invested,total_tokens,next_execution,created_at
                FROM dca_strategies WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DCAStrategy
	for rows.Next() {
		d := models.DCAStrategy{}
		_ = rows.Scan(&d.ID, &d.UserID, &d.TokenSymbol, &d.Network, &d.AmountBRL, &d.Frequency,
			&d.Active, &d.TotalInvested, &d.TotalTokens, &d.NextExecution, &d.CreatedAt)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (q *mobileQueries) UpdateDCA(ctx context.Context, id, userID string, active *bool, amount *float64, freq *models.DCAFrequency) error {
	if active != nil {
		_, err := q.sql.ExecContext(ctx, "UPDATE dca_strategies SET active=$1 WHERE id=$2 AND user_id=$3", *active, id, userID)
		if err != nil {
			return err
		}
	}
	if amount != nil {
		_, err := q.sql.ExecContext(ctx, "UPDATE dca_strategies SET amount_brl=$1 WHERE id=$2 AND user_id=$3", *amount, id, userID)
		if err != nil {
			return err
		}
	}
	if freq != nil {
		_, err := q.sql.ExecContext(ctx, "UPDATE dca_strategies SET frequency=$1, next_execution=$2 WHERE id=$3 AND user_id=$4",
			string(*freq), nextDCAExecution(*freq), id, userID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (q *mobileQueries) DeleteDCA(ctx context.Context, id, userID string) error {
	_, err := q.sql.ExecContext(ctx, "DELETE FROM dca_strategies WHERE id=$1 AND user_id=$2", id, userID)
	return err
}

func (q *mobileQueries) scanDCA(row *sql.Row) (*models.DCAStrategy, error) {
	d := &models.DCAStrategy{}
	err := row.Scan(&d.ID, &d.UserID, &d.TokenSymbol, &d.Network, &d.AmountBRL, &d.Frequency,
		&d.Active, &d.TotalInvested, &d.TotalTokens, &d.NextExecution, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func nextDCAExecution(freq models.DCAFrequency) time.Time {
	switch freq {
	case models.DCAWeekly:
		return time.Now().Add(7 * 24 * time.Hour)
	case models.DCAMonthly:
		return time.Now().AddDate(0, 1, 0)
	default:
		return time.Now().Add(24 * time.Hour)
	}
}

// ─── Notifications ────────────────────────────────────────────────────────────

func (q *mobileQueries) CreateNotification(ctx context.Context, userID, title, body, ntype string, data map[string]any) error {
	var dataStr *string
	if data != nil {
		if b, err := marshalJSON(data); err == nil {
			s := string(b)
			dataStr = &s
		}
	}
	_, err := q.sql.ExecContext(ctx, `
                INSERT INTO notifications (user_id, title, body, type, data)
                VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5)`,
		userID, title, body, ntype, dataStr)
	return err
}

func (q *mobileQueries) ListNotifications(ctx context.Context, userID string, limit int) ([]models.Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id,user_id,title,body,type,read,data,created_at
                FROM notifications WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Notification
	for rows.Next() {
		n := models.Notification{}
		_ = rows.Scan(&n.ID, &n.UserID, &n.Title, &n.Body, &n.Type, &n.Read, &n.Data, &n.CreatedAt)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (q *mobileQueries) GetNotification(ctx context.Context, userID, id string) (*models.Notification, error) {
	n := &models.Notification{}
	err := q.sql.QueryRowContext(ctx, `
                SELECT id,user_id,title,body,type,read,data,created_at
                FROM notifications WHERE user_id=$1 AND id=$2`, userID, id).
		Scan(&n.ID, &n.UserID, &n.Title, &n.Body, &n.Type, &n.Read, &n.Data, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (q *mobileQueries) MarkNotificationsRead(ctx context.Context, userID string, ids []string) error {
	if len(ids) == 0 {
		_, err := q.sql.ExecContext(ctx, "UPDATE notifications SET read=true WHERE user_id=$1", userID)
		return err
	}
	placeholders := ""
	args := []any{userID}
	for i, id := range ids {
		if placeholders != "" {
			placeholders += ","
		}
		placeholders += "$" + itoa(i+2)
		args = append(args, id)
	}
	_, err := q.sql.ExecContext(ctx, "UPDATE notifications SET read=true WHERE user_id=$1 AND id IN ("+placeholders+")", args...)
	return err
}

func (q *mobileQueries) DeleteNotification(ctx context.Context, userID, id string) error {
	_, err := q.sql.ExecContext(ctx, "DELETE FROM notifications WHERE id=$1 AND user_id=$2", id, userID)
	return err
}

// ─── Settings ─────────────────────────────────────────────────────────────────

func (q *mobileQueries) GetSettings(ctx context.Context, userID string) (*models.UserSettings, error) {
	s := &models.UserSettings{UserID: userID, DarkMode: true, Language: "pt-BR", Currency: "BRL", NotificationsEnabled: true, DailyLimit: 10000}
	err := q.sql.QueryRowContext(ctx, `
                SELECT user_id,dark_mode,language,currency,notifications_enabled,daily_limit
                FROM settings WHERE user_id=$1`, userID).Scan(
		&s.UserID, &s.DarkMode, &s.Language, &s.Currency, &s.NotificationsEnabled, &s.DailyLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil // return defaults
	}
	return s, err
}

func (q *mobileQueries) UpsertSettings(ctx context.Context, s *models.UserSettings) error {
	_, err := q.sql.ExecContext(ctx, `
                INSERT INTO settings (user_id, dark_mode, language, currency, notifications_enabled, daily_limit)
                VALUES ($1,$2,$3,$4,$5,$6)
                ON CONFLICT (user_id) DO UPDATE SET
                        dark_mode=$2, language=$3, currency=$4,
                        notifications_enabled=$5, daily_limit=$6`,
		s.UserID, s.DarkMode, s.Language, s.Currency, s.NotificationsEnabled, s.DailyLimit)
	return err
}

// ─── Orders (read-only — write goes through existing server) ──────────────────

func (q *mobileQueries) ListOrdersByUser(ctx context.Context, userID string, limit int) ([]map[string]any, error) {
	if err := q.ensureMobileGiftCardSchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := q.sql.QueryContext(ctx, `
                SELECT id, kind, amount_brl, crypto_amount, fee_brl, payout_brl,
                       status, asset, network, rate_locked, tx_hash, created_at, updated_at
                FROM (
                        SELECT id::text,
                               'sell'::text AS kind,
                               amount_brl::float8,
                               btc_amount::float8 AS crypto_amount,
                               COALESCE(fee_brl, 0)::float8 AS fee_brl,
                               COALESCE(payout_brl, 0)::float8 AS payout_brl,
                               status,
                               asset,
                               network,
                               rate_locked::float8,
                               tx_hash,
                               created_at,
                               updated_at
                        FROM orders
                        WHERE user_id=$1::uuid
                        UNION ALL
                        SELECT id::text,
                               'buy'::text AS kind,
                               amount_brl::float8,
                               crypto_amount::float8,
                               COALESCE(fee_brl, 0)::float8 AS fee_brl,
                               COALESCE(payout_brl, 0)::float8 AS payout_brl,
                               status,
                               asset,
                               COALESCE(network, 'BSC') AS network,
                               rate_locked::float8,
                               tx_hash_out AS tx_hash,
                               created_at,
                               updated_at
                        FROM buy_orders
                        WHERE user_id=$1::uuid
                        UNION ALL
                        SELECT id::text,
                               CASE
                                 WHEN payment_type='pix' THEN 'qr_pay'
                                 WHEN payment_type='payment_link' THEN 'payment_link'
                                 ELSE 'boleto'
                               END AS kind,
                               amount_brl::float8,
                               (required_usdt_micro::float8 / 1000000.0) AS crypto_amount,
                               COALESCE(fee_brl, 0)::float8 AS fee_brl,
                               0::float8 AS payout_brl,
                               status,
                               'USDT'::text AS asset,
                               'INTERNAL'::text AS network,
                               usdt_rate::float8 AS rate_locked,
                               provider_reference AS tx_hash,
                               created_at,
                               updated_at
                        FROM mobile_payment_intents
                        WHERE user_id=$1::uuid
                        UNION ALL
                        SELECT id::text,
                               CASE
                                 WHEN product_id LIKE '%voucher%' THEN 'crypto_voucher'
                                 ELSE 'gift_card'
                               END AS kind,
                               amount_brl::float8,
                               (required_usdt_micro::float8 / 1000000.0) AS crypto_amount,
                               COALESCE(fee_brl, 0)::float8 AS fee_brl,
                               0::float8 AS payout_brl,
                               status,
                               'USDT'::text AS asset,
                               'INTERNAL'::text AS network,
                               usdt_rate::float8 AS rate_locked,
                               provider_reference AS tx_hash,
                               created_at,
                               updated_at
                        FROM mobile_gift_card_orders
                        WHERE user_id=$1::uuid
                ) mobile_orders
                ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, kind, status, asset, network string
		var amtBRL, amtUSDT, feeBRL, payoutBRL, rateLocked float64
		var txHash *string
		var createdAt, updatedAt interface{}
		_ = rows.Scan(&id, &kind, &amtBRL, &amtUSDT, &feeBRL, &payoutBRL, &status, &asset, &network, &rateLocked, &txHash, &createdAt, &updatedAt)
		out = append(out, map[string]any{
			"id": id, "kind": kind, "status": status, "asset": asset, "network": network,
			"amountBrl": amtBRL, "amountCrypto": amtUSDT, "feeBrl": feeBRL,
			"payoutBrl": payoutBRL, "rateLocked": rateLocked,
			"txHash": txHash, "createdAt": createdAt, "updatedAt": updatedAt,
		})
	}
	return out, rows.Err()
}

// TagOrderUser sets user_id on an order (used after anonymous order creation).
func (q *mobileQueries) TagOrderUser(ctx context.Context, orderID, userID string) error {
	_, err := q.sql.ExecContext(ctx,
		"UPDATE orders SET user_id=$1::uuid WHERE id=$2::uuid AND user_id IS NULL", userID, orderID)
	return err
}

// TagBuyOrderUser sets user_id on a buy order created through the mobile API.
func (q *mobileQueries) TagBuyOrderUser(ctx context.Context, orderID, userID string) error {
	_, err := q.sql.ExecContext(ctx,
		"UPDATE buy_orders SET user_id=$1::uuid WHERE id=$2::uuid AND user_id IS NULL", userID, orderID)
	return err
}

func (q *mobileQueries) GetBuyOrderByUser(ctx context.Context, orderID, userID string) (map[string]any, error) {
	row := q.sql.QueryRowContext(ctx, `
                SELECT id, COALESCE(access_token, ''), request_id, status,
                       amount_brl::float8, COALESCE(amount_fiat, amount_brl)::float8,
                       COALESCE(fiat_currency, 'BRL'), COALESCE(payment_method, 'pix'), provider_payment_id,
                       COALESCE(fee_brl,0)::float8, COALESCE(payout_brl,0)::float8,
                       crypto_amount::float8, asset, COALESCE(network, 'BSC'), dest_address, rate_locked::float8, rate_lock_expires_at,
                       COALESCE(pix_payload, '{}'::jsonb), tx_hash_out, error, paid_at, settled_at, delivered_at, created_at, updated_at
                  FROM buy_orders
                 WHERE id=$1::uuid AND user_id=$2::uuid`, orderID, userID)
	var id, accessToken, status, fiatCurrency, paymentMethod, asset, network, destAddress string
	var requestID, providerPaymentID, txHashOut, errMsg sql.NullString
	var amountBRL, amountFiat, feeBRL, payoutBRL, cryptoAmount, rateLocked float64
	var rateLockExpiresAt, createdAt, updatedAt time.Time
	var pixPayload []byte
	var paidAt, settledAt, deliveredAt sql.NullTime
	if err := row.Scan(&id, &accessToken, &requestID, &status, &amountBRL, &amountFiat, &fiatCurrency, &paymentMethod, &providerPaymentID,
		&feeBRL, &payoutBRL, &cryptoAmount, &asset, &network, &destAddress, &rateLocked, &rateLockExpiresAt, &pixPayload, &txHashOut, &errMsg,
		&paidAt, &settledAt, &deliveredAt, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var payment map[string]any
	_ = json.Unmarshal(pixPayload, &payment)
	out := map[string]any{
		"id":                   id,
		"order_id":             id,
		"kind":                 "buy",
		"accessToken":          accessToken,
		"status":               status,
		"amount_brl":           amountBRL,
		"amountBRL":            amountBRL,
		"amount_fiat":          amountFiat,
		"amountFiat":           amountFiat,
		"fiat_currency":        fiatCurrency,
		"payment_method":       paymentMethod,
		"fee_brl":              feeBRL,
		"feeBRL":               feeBRL,
		"payout_brl":           payoutBRL,
		"payoutBRL":            payoutBRL,
		"crypto_amount":        cryptoAmount,
		"cryptoAmount":         cryptoAmount,
		"asset":                asset,
		"network":              network,
		"dest_address":         destAddress,
		"destAddress":          destAddress,
		"rate_locked":          rateLocked,
		"rateLocked":           rateLocked,
		"rate_lock_expires_at": rateLockExpiresAt,
		"pix_payload":          payment,
		"payment":              payment,
		"created_at":           createdAt,
		"updated_at":           updatedAt,
	}
	if requestID.Valid {
		out["request_id"] = requestID.String
	}
	if providerPaymentID.Valid {
		out["provider_payment_id"] = providerPaymentID.String
	}
	if txHashOut.Valid {
		out["tx_hash_out"] = txHashOut.String
	}
	if errMsg.Valid {
		out["error"] = errMsg.String
	}
	if paidAt.Valid {
		out["paid_at"] = paidAt.Time
	}
	if settledAt.Valid {
		out["settled_at"] = settledAt.Time
	}
	if deliveredAt.Valid {
		out["delivered_at"] = deliveredAt.Time
	}
	return out, nil
}

func (q *mobileQueries) CancelOrder(ctx context.Context, orderID, userID string) error {
	res, err := q.sql.ExecContext(ctx, `
                UPDATE orders SET status='cancelada', updated_at=NOW()
                WHERE id=$1 AND user_id=$2 AND status='aguardando_deposito'`, orderID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("ordem não encontrada ou não pode ser cancelada")
	}
	return nil
}
