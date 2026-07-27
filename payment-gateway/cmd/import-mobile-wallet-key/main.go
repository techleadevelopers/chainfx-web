package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/privacy"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	_ "github.com/lib/pq"
)

type walletFile struct {
	Wallets []struct {
		Label      string `json:"label"`
		Address    string `json:"address"`
		PrivateKey string `json:"privateKey"`
	} `json:"wallets"`
}

func main() {
	var email, address, walletPath, privateKey string
	flag.StringVar(&email, "email", "", "mobile user email")
	flag.StringVar(&address, "address", "", "wallet address to import")
	flag.StringVar(&walletPath, "wallet-file", "", "optional contracts/wallets/system-wallets-*.json file")
	flag.StringVar(&privateKey, "private-key", "", "optional private key; prefer MOBILE_WALLET_IMPORT_PRIVATE_KEY env")
	flag.Parse()

	email = strings.TrimSpace(strings.ToLower(email))
	address = strings.TrimSpace(address)
	if email == "" || !strings.Contains(email, "@") {
		log.Fatal("provide --email")
	}
	if !common.IsHexAddress(address) {
		log.Fatal("provide valid --address")
	}
	checksummed := common.HexToAddress(address).Hex()

	if privateKey == "" {
		privateKey = strings.TrimSpace(os.Getenv("MOBILE_WALLET_IMPORT_PRIVATE_KEY"))
	}
	if privateKey == "" && walletPath != "" {
		var err error
		privateKey, err = privateKeyFromWalletFile(walletPath, checksummed)
		if err != nil {
			log.Fatal(err)
		}
	}
	if strings.TrimSpace(privateKey) == "" {
		log.Fatal("provide --wallet-file or MOBILE_WALLET_IMPORT_PRIVATE_KEY")
	}

	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKey), "0x"))
	if err != nil {
		log.Fatalf("invalid private key: %v", err)
	}
	derived := crypto.PubkeyToAddress(key.PublicKey).Hex()
	if !strings.EqualFold(derived, checksummed) {
		log.Fatalf("private key does not match address: derived %s", derived)
	}

	cfg := config.LoadConfig()
	codec, err := privacy.New(firstNonEmpty(os.Getenv("MOBILE_WALLET_ENCRYPTION_SECRET"), cfg.LGPDSecret, cfg.WebhookSecret))
	if err != nil {
		log.Fatalf("wallet encryption secret unavailable: %v", err)
	}
	encryptedKey, err := codec.Encrypt("0x" + strings.TrimPrefix(strings.TrimSpace(privateKey), "0x"))
	if err != nil {
		log.Fatalf("encrypt private key: %v", err)
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		log.Fatal("DATABASE_URL not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := importKey(ctx, db, email, checksummed, encryptedKey); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Imported custodial mobile wallet key for %s (%s).\n", email, checksummed)
}

func privateKeyFromWalletFile(path, address string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read wallet file: %w", err)
	}
	var payload walletFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("parse wallet file: %w", err)
	}
	for _, wallet := range payload.Wallets {
		if strings.EqualFold(wallet.Address, address) {
			return wallet.PrivateKey, nil
		}
	}
	return "", fmt.Errorf("address %s not found in wallet file", address)
}

func importKey(ctx context.Context, db *sql.DB, email, address, encryptedKey string) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS mobile_wallet_keys (
  user_id               UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  wallet_address        TEXT        NOT NULL UNIQUE,
  encrypted_private_key TEXT        NOT NULL,
  custody_mode          TEXT        NOT NULL DEFAULT 'system_custody',
  network               TEXT        NOT NULL DEFAULT 'EVM',
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_wallet_unique
  ON users (lower(wallet_address))
  WHERE wallet_address IS NOT NULL AND wallet_address <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_mobile_wallet_keys_address
  ON mobile_wallet_keys (lower(wallet_address));`); err != nil {
		return fmt.Errorf("ensure wallet schema: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var userID string
	if err := tx.QueryRowContext(ctx, `
		SELECT id::text
		  FROM users
		 WHERE lower(email)=lower($1)
		   AND deleted_at IS NULL
		 FOR UPDATE`, email).Scan(&userID); err != nil {
		return fmt.Errorf("user not found: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO mobile_wallet_keys (user_id, wallet_address, encrypted_private_key)
		VALUES ($1::uuid, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET
		  wallet_address=EXCLUDED.wallet_address,
		  encrypted_private_key=EXCLUDED.encrypted_private_key,
		  custody_mode='system_custody',
		  network='EVM',
		  updated_at=NOW()`, userID, address, encryptedKey); err != nil {
		return fmt.Errorf("upsert wallet key: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users
		   SET wallet_address=$1,
		       updated_at=NOW()
		 WHERE id=$2::uuid`, address, userID); err != nil {
		return fmt.Errorf("update user wallet: %w", err)
	}
	return tx.Commit()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
