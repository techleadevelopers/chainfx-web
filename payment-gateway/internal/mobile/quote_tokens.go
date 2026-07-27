package mobile

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type mobileQuoteClaims struct {
	Side      string  `json:"side"`
	Asset     string  `json:"asset"`
	Network   string  `json:"network,omitempty"`
	Amount    float64 `json:"amount"`
	Rate      float64 `json:"rate"`
	Fee       float64 `json:"fee"`
	Total     float64 `json:"total"`
	ExpiresAt int64   `json:"exp"`
}

func (s *Server) issueMobileQuote(claims mobileQuoteClaims) (string, error) {
	claims.Side = strings.ToLower(strings.TrimSpace(claims.Side))
	claims.Asset = strings.ToUpper(strings.TrimSpace(claims.Asset))
	claims.Network = strings.ToUpper(strings.TrimSpace(claims.Network))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(s.mobileQuoteSecret()))
	_, _ = mac.Write([]byte(encodedPayload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "mq_" + encodedPayload + "." + signature, nil
}

func (s *Server) verifyMobileQuote(raw, side, asset string, amount float64, now time.Time, networks ...string) (*mobileQuoteClaims, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "mq_") {
		return nil, fmt.Errorf("quote_id invalido")
	}
	parts := strings.Split(strings.TrimPrefix(raw, "mq_"), ".")
	if len(parts) != 2 {
		return nil, fmt.Errorf("quote_id invalido")
	}
	mac := hmac.New(sha256.New, []byte(s.mobileQuoteSecret()))
	_, _ = mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, expected) {
		return nil, fmt.Errorf("quote_id assinatura invalida")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("quote_id payload invalido")
	}
	var claims mobileQuoteClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("quote_id payload invalido")
	}
	if !strings.EqualFold(claims.Side, side) || !strings.EqualFold(claims.Asset, asset) {
		return nil, fmt.Errorf("quote_id nao corresponde a operacao")
	}
	if len(networks) > 0 && strings.TrimSpace(claims.Network) != "" && !strings.EqualFold(claims.Network, networks[0]) {
		return nil, fmt.Errorf("quote_id nao corresponde a rede")
	}
	if math.Abs(claims.Amount-amount) > 0.000001 {
		return nil, fmt.Errorf("quote_id nao corresponde ao valor")
	}
	if claims.ExpiresAt <= now.UTC().Unix() {
		return nil, fmt.Errorf("quote_id expirado")
	}
	return &claims, nil
}

func (s *Server) mobileQuoteSecret() string {
	if s != nil && s.mcfg != nil && strings.TrimSpace(s.mcfg.JWTSecret) != "" {
		return s.mcfg.JWTSecret
	}
	return "mobile_quote_development_secret_change_me"
}
