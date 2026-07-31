package mobile

import (
	"context"
	"crypto/ecdsa"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"payment-gateway/internal/privacy"

	"github.com/ethereum/go-ethereum/crypto"
)

var (
	errMobileWalletKeyUnavailable = errors.New("mobile wallet key unavailable")
	errMobileWalletKeyMismatch    = errors.New("mobile wallet key/address mismatch")
)

type mobileWalletEncryptionKeyring struct {
	activeID string
	keys     map[string]string
}

func (s *Server) mobileWalletEncryptionKeyring() (*mobileWalletEncryptionKeyring, error) {
	activeID := strings.TrimSpace(envOr("MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID", ""))
	keys, err := parseMobileWalletEncryptionKeys(envOr("MOBILE_WALLET_ENCRYPTION_KEYS", ""))
	if err != nil {
		return nil, err
	}
	if len(keys) > 0 {
		if activeID == "" {
			return nil, fmt.Errorf("mobile wallet encryption keyring requires MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID")
		}
		if strings.TrimSpace(keys[activeID]) == "" {
			return nil, fmt.Errorf("mobile wallet encryption active key %q not found", activeID)
		}
		return &mobileWalletEncryptionKeyring{activeID: activeID, keys: keys}, nil
	}

	if s != nil && s.cfg != nil && s.cfg.IsProduction() {
		return nil, fmt.Errorf("mobile wallet encryption keyring not configured")
	}
	legacySecret := strings.TrimSpace(s.mobileWalletLegacyEncryptionSecret())
	if legacySecret == "" {
		return nil, fmt.Errorf("mobile wallet encryption secret not configured")
	}
	legacyID := strings.TrimSpace(envOr("MOBILE_WALLET_ENCRYPTION_LEGACY_KEY_ID", "legacy"))
	return &mobileWalletEncryptionKeyring{
		activeID: legacyID,
		keys:     map[string]string{legacyID: legacySecret},
	}, nil
}

func parseMobileWalletEncryptionKeys(raw string) (map[string]string, error) {
	keys := map[string]string{}
	raw = strings.ReplaceAll(raw, "\n", ",")
	raw = strings.ReplaceAll(raw, ";", ",")
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, secret, ok := strings.Cut(part, ":")
		id = strings.TrimSpace(id)
		secret = strings.TrimSpace(secret)
		if !ok || id == "" || secret == "" {
			return nil, fmt.Errorf("invalid MOBILE_WALLET_ENCRYPTION_KEYS entry")
		}
		if _, exists := keys[id]; exists {
			return nil, fmt.Errorf("duplicate mobile wallet encryption key id %q", id)
		}
		keys[id] = secret
	}
	return keys, nil
}

func (k *mobileWalletEncryptionKeyring) encrypt(plain string) (ciphertext, keyID string, version int, err error) {
	if k == nil || strings.TrimSpace(k.activeID) == "" {
		return "", "", 0, errMobileWalletKeyUnavailable
	}
	secret := strings.TrimSpace(k.keys[k.activeID])
	if secret == "" {
		return "", "", 0, errMobileWalletKeyUnavailable
	}
	codec, err := privacy.New(secret)
	if err != nil {
		return "", "", 0, err
	}
	ciphertext, err = codec.Encrypt(plain)
	if err != nil {
		return "", "", 0, err
	}
	return ciphertext, k.activeID, 1, nil
}

func (k *mobileWalletEncryptionKeyring) decrypt(record mobileWalletKey) (string, error) {
	keyID := strings.TrimSpace(record.EncryptionKeyID)
	if keyID == "" {
		keyID = strings.TrimSpace(envOr("MOBILE_WALLET_ENCRYPTION_LEGACY_KEY_ID", "legacy"))
	}
	if k == nil || strings.TrimSpace(k.keys[keyID]) == "" {
		return "", fmt.Errorf("%w: %s", errMobileWalletKeyUnavailable, keyID)
	}
	codec, err := privacy.New(k.keys[keyID])
	if err != nil {
		return "", err
	}
	plain, err := codec.Decrypt(record.EncryptedPrivateKey)
	if err != nil {
		return "", fmt.Errorf("%w: decrypt failed for key_id=%s", errMobileWalletKeyUnavailable, keyID)
	}
	return plain, nil
}

func (s *Server) encryptMobileWalletPrivateKey(privateKeyHex string) (ciphertext, keyID string, version int, err error) {
	keyring, err := s.mobileWalletEncryptionKeyring()
	if err != nil {
		return "", "", 0, err
	}
	return keyring.encrypt(privateKeyHex)
}

func (s *Server) decryptMobileWalletSigningKey(record mobileWalletKey, expectedAddress string) (*ecdsa.PrivateKey, error) {
	keyring, err := s.mobileWalletEncryptionKeyring()
	if err != nil {
		return nil, err
	}
	privateKeyHex, err := keyring.decrypt(record)
	if err != nil {
		slog.Warn("mobile wallet decrypt failed", "rail", "EVM", "wallet_key_id", record.EncryptionKeyID, "error_class", "wallet_key_unavailable")
		return nil, err
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return nil, fmt.Errorf("chave custodial invalida")
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	if !strings.EqualFold(address, expectedAddress) {
		slog.Warn("mobile wallet key/address mismatch", "rail", "EVM", "wallet_key_id", record.EncryptionKeyID, "error_class", "wallet_key_mismatch")
		return nil, errMobileWalletKeyMismatch
	}
	return key, nil
}

func (s *Server) reencryptMobileWalletKeyMaterial(record mobileWalletKey, expectedAddress string) (encryptedPrivateKey, keyID string, version int, err error) {
	keyring, err := s.mobileWalletEncryptionKeyring()
	if err != nil {
		return "", "", 0, err
	}
	privateKeyHex, err := keyring.decrypt(record)
	if err != nil {
		return "", "", 0, err
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return "", "", 0, fmt.Errorf("chave custodial invalida")
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	if !strings.EqualFold(address, expectedAddress) {
		return "", "", 0, errMobileWalletKeyMismatch
	}
	return keyring.encrypt(privateKeyHex)
}

func (s *Server) reencryptMobileWalletKey(ctx context.Context, userID, walletAddress string) error {
	if s == nil || s.db == nil || s.db.SQL == nil {
		return errMobileWalletKeyUnavailable
	}
	q := mobileDB(s.db)
	if err := q.ensureMobileWalletKeySchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	record := mobileWalletKey{}
	err = tx.QueryRowContext(ctx, `
                SELECT user_id::text,
                       wallet_address,
                       encrypted_private_key,
                       COALESCE(NULLIF(encryption_key_id, ''), 'legacy'),
                       COALESCE(NULLIF(encryption_version, 0), 1),
                       custody_mode,
                       network
                  FROM mobile_wallet_keys
                 WHERE user_id=$1::uuid
                   AND lower(wallet_address)=lower($2)
                 FOR UPDATE`,
		userID, walletAddress).Scan(&record.UserID, &record.WalletAddress, &record.EncryptedPrivateKey, &record.EncryptionKeyID, &record.EncryptionVersion, &record.CustodyMode, &record.Network)
	if errors.Is(err, sql.ErrNoRows) {
		return errMobileWalletKeyUnavailable
	}
	if err != nil {
		return err
	}
	encrypted, keyID, version, err := s.reencryptMobileWalletKeyMaterial(record, walletAddress)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
                UPDATE mobile_wallet_keys
                   SET encrypted_private_key=$1,
                       encryption_key_id=$2,
                       encryption_version=$3,
                       updated_at=NOW()
                 WHERE user_id=$4::uuid
                   AND lower(wallet_address)=lower($5)`,
		encrypted, keyID, version, userID, walletAddress)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) mobileWalletLegacyEncryptionSecret() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	if secret := strings.TrimSpace(envOr("MOBILE_WALLET_ENCRYPTION_SECRET", "")); secret != "" {
		return secret
	}
	if secret := strings.TrimSpace(s.cfg.LGPDSecret); secret != "" {
		return secret
	}
	if secret := strings.TrimSpace(s.cfg.WebhookSecret); secret != "" {
		return secret
	}
	return strings.TrimSpace(s.mcfg.JWTSecret)
}
