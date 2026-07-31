package solana

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	ErrWalletKeyUnavailable = errors.New("solana: wallet_key_unavailable")
	ErrSignerKeyMismatch    = errors.New("solana: signer pubkey mismatch")
)

type derivationKeyring struct {
	activeID string
	keys     map[string]string
	legacyID string
}

func newDerivationKeyring(activeID, rawKeys, legacyID, legacySecret string, production bool) (*derivationKeyring, error) {
	keys, err := parseDerivationKeys(rawKeys)
	if err != nil {
		return nil, err
	}
	activeID = strings.TrimSpace(activeID)
	legacyID = strings.TrimSpace(legacyID)
	if legacyID == "" {
		legacyID = "legacy"
	}
	if len(keys) > 0 {
		if activeID == "" {
			return nil, fmt.Errorf("solana: SOL_WALLET_ACTIVE_DERIVATION_KEY_ID obrigatorio")
		}
		if strings.TrimSpace(keys[activeID]) == "" {
			return nil, fmt.Errorf("solana: active derivation key %q ausente", activeID)
		}
		return &derivationKeyring{activeID: activeID, keys: keys, legacyID: legacyID}, nil
	}
	if production {
		return nil, fmt.Errorf("solana: derivation keyring nao configurado")
	}
	legacySecret = strings.TrimSpace(legacySecret)
	if len(legacySecret) < 32 {
		return nil, ErrSigningNotConfigured
	}
	id := derivationKeyIDForSecret(legacySecret)
	return &derivationKeyring{
		activeID: id,
		keys:     map[string]string{id: legacySecret, legacyID: legacySecret},
		legacyID: legacyID,
	}, nil
}

func newDerivationKeyringFromEnv(legacySecret string, production bool) (*derivationKeyring, error) {
	return newDerivationKeyring(
		os.Getenv("SOL_WALLET_ACTIVE_DERIVATION_KEY_ID"),
		os.Getenv("SOL_WALLET_DERIVATION_KEYS"),
		os.Getenv("SOL_WALLET_LEGACY_DERIVATION_KEY_ID"),
		legacySecret,
		production,
	)
}

func parseDerivationKeys(raw string) (map[string]string, error) {
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
			return nil, fmt.Errorf("solana: invalid SOL_WALLET_DERIVATION_KEYS entry")
		}
		if len(secret) < 32 {
			return nil, fmt.Errorf("solana: derivation key %q must have at least 32 bytes", id)
		}
		if _, exists := keys[id]; exists {
			return nil, fmt.Errorf("solana: duplicate derivation key id %q", id)
		}
		keys[id] = secret
	}
	return keys, nil
}

func (k *derivationKeyring) secretFor(keyID string) (string, error) {
	if k == nil {
		return "", ErrWalletKeyUnavailable
	}
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		keyID = strings.TrimSpace(k.legacyID)
	}
	if keyID == "" {
		return "", ErrWalletKeyUnavailable
	}
	secret := strings.TrimSpace(k.keys[keyID])
	if secret == "" {
		return "", fmt.Errorf("%w: %s", ErrWalletKeyUnavailable, keyID)
	}
	return secret, nil
}

func (k *derivationKeyring) derivePrivateKey(userID, keyID string) (ed25519.PrivateKey, error) {
	secret, err := k.secretFor(keyID)
	if err != nil {
		return nil, err
	}
	return derivePrivateKeyWithSecret(userID, secret)
}

func derivePrivateKeyWithSecret(userID, secret string) (ed25519.PrivateKey, error) {
	secret = strings.TrimSpace(secret)
	if len(secret) < 32 {
		return nil, ErrSigningNotConfigured
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("chainfx-solana-wallet-v1:" + strings.TrimSpace(userID)))
	seed := mac.Sum(nil)
	return ed25519.NewKeyFromSeed(seed[:32]), nil
}

func derivationKeyIDForSecret(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return "hmac-sha256:" + hex.EncodeToString(sum[:8])
}
