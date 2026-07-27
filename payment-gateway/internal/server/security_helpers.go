package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func validHMAC(secret string, raw []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature), []byte(expected))
}
