package nfc

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const TokenPrefix = "nfc1"

var (
	ErrSecretRequired = errors.New("nfc: token secret is required")
	ErrInvalidToken   = errors.New("nfc: invalid token")
	ErrExpiredToken   = errors.New("nfc: token expired")
)

type TokenClaims struct {
	TokenID       string `json:"tid"`
	Wallet        string `json:"wallet"`
	DeviceID      string `json:"device_id"`
	Network       string `json:"network"`
	IssuedAtUnix  int64  `json:"iat"`
	ExpiresAtUnix int64  `json:"exp"`
	Nonce         string `json:"nonce"`
}

func IssueToken(secret, wallet, deviceID, network string, ttl time.Duration, now time.Time) (string, TokenClaims, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", TokenClaims{}, ErrSecretRequired
	}
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	claims := TokenClaims{
		TokenID:       newRandomHex(16),
		Wallet:        strings.ToLower(strings.TrimSpace(wallet)),
		DeviceID:      strings.TrimSpace(deviceID),
		Network:       strings.ToUpper(strings.TrimSpace(network)),
		IssuedAtUnix:  now.UTC().Unix(),
		ExpiresAtUnix: now.UTC().Add(ttl).Unix(),
		Nonce:         newRandomHex(12),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", TokenClaims{}, err
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	sig := sign(secret, payloadPart)
	return TokenPrefix + "." + payloadPart + "." + sig, claims, nil
}

func VerifyToken(secret, token string, now time.Time) (TokenClaims, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return TokenClaims{}, ErrSecretRequired
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != TokenPrefix {
		return TokenClaims{}, ErrInvalidToken
	}
	want := sign(secret, parts[1])
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return TokenClaims{}, ErrInvalidToken
	}
	expected, err := base64.RawURLEncoding.DecodeString(want)
	if err != nil || !hmac.Equal(got, expected) {
		return TokenClaims{}, ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TokenClaims{}, ErrInvalidToken
	}
	var claims TokenClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return TokenClaims{}, ErrInvalidToken
	}
	if claims.TokenID == "" || claims.Wallet == "" || claims.ExpiresAtUnix <= 0 {
		return TokenClaims{}, ErrInvalidToken
	}
	if !now.UTC().Before(time.Unix(claims.ExpiresAtUnix, 0)) {
		return TokenClaims{}, ErrExpiredToken
	}
	claims.Wallet = strings.ToLower(strings.TrimSpace(claims.Wallet))
	claims.Network = strings.ToUpper(strings.TrimSpace(claims.Network))
	return claims, nil
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func sign(secret, payloadPart string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payloadPart))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func newRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
