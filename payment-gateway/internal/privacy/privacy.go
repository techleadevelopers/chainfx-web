package privacy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

type Codec struct {
	key []byte
}

func New(secret string) (*Codec, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("LGPD_SECRET nao configurado")
	}
	sum := sha256.Sum256([]byte(secret))
	return &Codec{key: sum[:]}, nil
}

func Hash(value, secret string) string {
	value = normalize(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret) + ":" + value))
	return hex.EncodeToString(sum[:])
}

func (c *Codec) Encrypt(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	raw := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(raw), nil
}

func (c *Codec) Decrypt(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext invalido")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func normalize(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.NewReplacer(".", "", "-", "", " ", "", "(", "", ")", "", "+", "").Replace(value)
	return value
}

// ─── PII masking helpers (LGPD compliance for admin/dashboard views) ──────────

// MaskCPF redacts all but the last 3 digits of a CPF string.
// Input may have punctuation (e.g. "123.456.789-09"); output is "***.***.***-09".
func MaskCPF(cpf string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.TrimSpace(cpf))
	if len(digits) < 4 {
		return "***"
	}
	suffix := digits[len(digits)-3:]
	return "***.***.***-" + suffix
}

// MaskPhone keeps only the last 4 digits of a phone number.
func MaskPhone(phone string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.TrimSpace(phone))
	if len(digits) < 4 {
		return "***"
	}
	return "(**) *****-" + digits[len(digits)-4:]
}

// MaskEmail keeps the first character, masks the local part, and preserves the domain.
func MaskEmail(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return email[:1] + "***"
	}
	local := email[:at]
	domain := email[at:]
	if len(local) <= 1 {
		return local + "***" + domain
	}
	return local[:1] + strings.Repeat("*", minLen(len(local)-1, 4)) + domain
}

// MaskPixKey masks a PIX key that may be a CPF, phone, e-mail or EVP UUID.
// It auto-detects the type by length/format heuristic.
func MaskPixKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if strings.Contains(key, "@") {
		return MaskEmail(key)
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, key)
	switch {
	case len(digits) == 11: // CPF
		return MaskCPF(key)
	case len(digits) >= 10 && len(digits) <= 13: // phone
		return MaskPhone(key)
	default: // EVP / random key
		if len(key) > 8 {
			return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
		}
		return "****"
	}
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}
