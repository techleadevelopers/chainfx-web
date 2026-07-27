package kyc_engine

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func EncryptEmbedding(secret string, embedding []float32) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("FACE_BIOMETRY_SECRET ou LGPD_SECRET obrigatorio para criptografar biometria")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(embedding)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plain, []byte(Version))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func DecryptEmbedding(secret, encrypted string) ([]float32, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("segredo de biometria ausente")
	}
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("payload biometrico invalido")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(Version))
	if err != nil {
		return nil, err
	}
	var embedding []float32
	if err := json.Unmarshal(plain, &embedding); err != nil {
		return nil, err
	}
	return embedding, nil
}

func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrt(normA) * sqrt(normB))
}

func SimilarityPercent(a, b []float32) int {
	sim := (CosineSimilarity(a, b) + 1) / 2
	return clamp(int(sim*100), 0, 100)
}

func sqrt(v float64) float64 {
	z := v
	for i := 0; i < 12; i++ {
		z -= (z*z - v) / (2 * z)
	}
	return z
}
