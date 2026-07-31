package mobile

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"payment-gateway/internal/config"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestMobileWalletEncryptionKeyringRotation(t *testing.T) {
	t.Setenv("MOBILE_WALLET_ENCRYPTION_KEYS", "v1:01234567890123456789012345678901")
	t.Setenv("MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID", "v1")

	s := testMobileServerForKeyring()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	privateKeyHex := "0x" + hex.EncodeToString(crypto.FromECDSA(key))
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()

	encrypted, keyID, version, err := s.encryptMobileWalletPrivateKey(privateKeyHex)
	if err != nil {
		t.Fatalf("encrypt v1: %v", err)
	}
	if keyID != "v1" || version != 1 {
		t.Fatalf("key metadata=%s/%d want v1/1", keyID, version)
	}
	record := mobileWalletKey{WalletAddress: address, EncryptedPrivateKey: encrypted, EncryptionKeyID: keyID, EncryptionVersion: version}
	if _, err := s.decryptMobileWalletSigningKey(record, address); err != nil {
		t.Fatalf("decrypt v1: %v", err)
	}

	t.Setenv("MOBILE_WALLET_ENCRYPTION_KEYS", "v1:01234567890123456789012345678901,v2:abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID", "v2")
	if _, err := s.decryptMobileWalletSigningKey(record, address); err != nil {
		t.Fatalf("decrypt old wallet with old+new keyring: %v", err)
	}
	encrypted2, keyID2, _, err := s.encryptMobileWalletPrivateKey(privateKeyHex)
	if err != nil {
		t.Fatalf("encrypt v2: %v", err)
	}
	if keyID2 != "v2" || encrypted2 == encrypted {
		t.Fatalf("new encryption did not use active v2")
	}

	t.Setenv("MOBILE_WALLET_ENCRYPTION_KEYS", "v2:abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID", "v2")
	if _, err := s.decryptMobileWalletSigningKey(record, address); !errors.Is(err, errMobileWalletKeyUnavailable) {
		t.Fatalf("err=%v want errMobileWalletKeyUnavailable", err)
	}
}

func TestMobileWalletSigningKeyMustMatchPersistedAddress(t *testing.T) {
	t.Setenv("MOBILE_WALLET_ENCRYPTION_KEYS", "v1:01234567890123456789012345678901")
	t.Setenv("MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID", "v1")

	s := testMobileServerForKeyring()
	key, _ := crypto.GenerateKey()
	privateKeyHex := "0x" + hex.EncodeToString(crypto.FromECDSA(key))
	encrypted, keyID, version, err := s.encryptMobileWalletPrivateKey(privateKeyHex)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	record := mobileWalletKey{EncryptedPrivateKey: encrypted, EncryptionKeyID: keyID, EncryptionVersion: version}
	if _, err := s.decryptMobileWalletSigningKey(record, "0x0000000000000000000000000000000000000001"); !errors.Is(err, errMobileWalletKeyMismatch) {
		t.Fatalf("err=%v want errMobileWalletKeyMismatch", err)
	}
}

func TestMobileWalletReencryptKeepsSameAddressAndUsesActiveKey(t *testing.T) {
	t.Setenv("MOBILE_WALLET_ENCRYPTION_KEYS", "v1:01234567890123456789012345678901")
	t.Setenv("MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID", "v1")

	s := testMobileServerForKeyring()
	key, _ := crypto.GenerateKey()
	privateKeyHex := "0x" + hex.EncodeToString(crypto.FromECDSA(key))
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	encrypted, keyID, version, err := s.encryptMobileWalletPrivateKey(privateKeyHex)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	t.Setenv("MOBILE_WALLET_ENCRYPTION_KEYS", "v1:01234567890123456789012345678901,v2:abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID", "v2")
	reEncrypted, newKeyID, newVersion, err := s.reencryptMobileWalletKeyMaterial(
		mobileWalletKey{WalletAddress: address, EncryptedPrivateKey: encrypted, EncryptionKeyID: keyID, EncryptionVersion: version},
		address,
	)
	if err != nil {
		t.Fatalf("reencrypt: %v", err)
	}
	if newKeyID != "v2" || newVersion != 1 {
		t.Fatalf("new metadata=%s/%d want v2/1", newKeyID, newVersion)
	}
	if strings.TrimSpace(reEncrypted) == "" || reEncrypted == encrypted {
		t.Fatalf("reencrypted blob was not replaced")
	}
	if _, err := s.decryptMobileWalletSigningKey(mobileWalletKey{EncryptedPrivateKey: reEncrypted, EncryptionKeyID: newKeyID, EncryptionVersion: newVersion}, address); err != nil {
		t.Fatalf("decrypt reencrypted: %v", err)
	}
}

func TestMobileWalletKeyringProductionRequiresExplicitKeys(t *testing.T) {
	t.Setenv("MOBILE_WALLET_ENCRYPTION_KEYS", "")
	t.Setenv("MOBILE_WALLET_ENCRYPTION_ACTIVE_KEY_ID", "")
	t.Setenv("MOBILE_WALLET_ENCRYPTION_SECRET", "")
	s := &Server{cfg: &config.Config{Environment: "production"}, mcfg: &MobileConfig{JWTSecret: strings.Repeat("j", 32)}}
	if _, err := s.mobileWalletEncryptionKeyring(); err == nil {
		t.Fatal("production keyring without explicit keys should fail")
	}
}

func testMobileServerForKeyring() *Server {
	return &Server{
		cfg:  &config.Config{Environment: "development", LGPDSecret: strings.Repeat("l", 32), WebhookSecret: strings.Repeat("w", 32)},
		mcfg: &MobileConfig{JWTSecret: strings.Repeat("j", 32)},
	}
}
