package nfc

import (
	"strings"
	"testing"
	"time"
)

func TestIssueAndVerifyToken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	token, claims, err := IssueToken("secret", "0xABC", "device-1", "bsc", time.Minute, now)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	got, err := VerifyToken("secret", token, now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("VerifyToken() error = %v", err)
	}
	if got.TokenID != claims.TokenID {
		t.Fatalf("token id mismatch: %s != %s", got.TokenID, claims.TokenID)
	}
	if got.Wallet != "0xabc" || got.Network != "BSC" {
		t.Fatalf("claims not normalized: %+v", got)
	}
}

func TestVerifyTokenRejectsTamperAndExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	token, _, err := IssueToken("secret", "0xabc", "device-1", "BSC", time.Minute, now)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	if _, err := VerifyToken("other-secret", token, now); err != ErrInvalidToken {
		t.Fatalf("expected invalid token for wrong secret, got %v", err)
	}
	if _, err := VerifyToken("secret", token, now.Add(2*time.Minute)); err != ErrExpiredToken {
		t.Fatalf("expected expired token, got %v", err)
	}
}

func TestVerifyTokenRejectsPayloadTampering(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	token, _, err := IssueToken("secret", "0xabc", "device-1", "BSC", time.Minute, now)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token format: %s", token)
	}
	tamperedPayload := parts[1][:len(parts[1])-1] + "A"
	if tamperedPayload == parts[1] {
		tamperedPayload = parts[1][:len(parts[1])-1] + "B"
	}
	tampered := parts[0] + "." + tamperedPayload + "." + parts[2]
	if _, err := VerifyToken("secret", tampered, now); err != ErrInvalidToken {
		t.Fatalf("expected invalid token after tamper, got %v", err)
	}
}

func TestIssueTokenDefaultsTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, claims, err := IssueToken("secret", "0xabc", "device-1", "bsc", 0, now)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	if got := claims.ExpiresAtUnix - claims.IssuedAtUnix; got != int64((2 * time.Minute).Seconds()) {
		t.Fatalf("expected default ttl 120s, got %d", got)
	}
}
