package mobile

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/email"

	"golang.org/x/crypto/bcrypt"
)

// ─── JWT (HS256, stdlib only) ─────────────────────────────────────────────────

type jwtClaims struct {
	Sub  string `json:"sub"`
	Exp  int64  `json:"exp"`
	Iat  int64  `json:"iat"`
	Type string `json:"type"` // "access" | "refresh"
}

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func issueToken(secret string, claims jwtClaims) (string, error) {
	header := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := header + "." + b64url(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return body + "." + b64url(mac.Sum(nil)), nil
}

func verifyToken(secret, token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}
	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	expected := b64url(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return nil, fmt.Errorf("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims jwtClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	if time.Now().Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}
	return &claims, nil
}

func (s *Server) newAccessToken(userID string) (string, error) {
	exp := time.Now().Add(time.Duration(s.mcfg.JWTExpiresMin) * time.Minute).Unix()
	return issueToken(s.mcfg.JWTSecret, jwtClaims{Sub: userID, Exp: exp, Iat: time.Now().Unix(), Type: "access"})
}

func (s *Server) newRefreshToken(userID string) (string, error) {
	exp := time.Now().Add(time.Duration(s.mcfg.RefreshExpiresDays) * 24 * time.Hour).Unix()
	return issueToken(s.mcfg.RefreshSecret, jwtClaims{Sub: userID, Exp: exp, Iat: time.Now().Unix(), Type: "refresh"})
}

func (s *Server) newEmailVerificationToken(emailAddress string) (string, error) {
	exp := time.Now().Add(15 * time.Minute).Unix()
	return issueToken(s.mcfg.JWTSecret, jwtClaims{
		Sub:  strings.TrimSpace(strings.ToLower(emailAddress)),
		Exp:  exp,
		Iat:  time.Now().Unix(),
		Type: "email_verification",
	})
}

// requireAuth middleware — injects user ID into context.
type contextKey string

const ctxUserID contextKey = "uid"

// userActiveCacheTTL keeps the active-user check warm without leaving a long
// fraud window after account deletion or lockout operations.
const userActiveCacheTTL = 5 * time.Second

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token não informado"})
			return
		}
		claims, err := verifyToken(s.mcfg.JWTSecret, auth[7:])
		if err != nil || claims.Type != "access" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token inválido ou expirado"})
			return
		}
		if s.db != nil {
			cacheKey := "user_active:" + claims.Sub
			if cached, ok := s.getMobileCache(cacheKey); ok {
				if active, _ := cached.(bool); !active {
					writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "usuario nao encontrado ou conta excluida"})
					return
				}
			} else {
				active, err := mobileDB(s.db).IsUserActive(r.Context(), claims.Sub)
				if err != nil || !active {
					// Cache negative result briefly to avoid hammering DB on bad tokens.
					s.setMobileCache(cacheKey, false, 5*time.Second)
					writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "usuario nao encontrado ou conta excluida"})
					return
				}
				s.setMobileCache(cacheKey, true, userActiveCacheTTL)
			}
		}
		ctx := context.WithValue(r.Context(), ctxUserID, claims.Sub)
		next(w, r.WithContext(ctx))
	}
}

// invalidateUserActiveCache removes the cached active status for a user,
// e.g. after logout or account deletion so the next request re-checks the DB.
func (s *Server) invalidateUserActiveCache(userID string) {
	s.cacheMu.Lock()
	delete(s.cache, "user_active:"+userID)
	s.cacheMu.Unlock()
}

func userIDFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxUserID).(string)
	return v
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

const registerEmailOTPPurpose = "mobile_register"

func (s *Server) handleRegisterRequestCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	to := strings.TrimSpace(strings.ToLower(req.Email))
	if to == "" || !strings.Contains(to, "@") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "email invalido"})
		return
	}
	if user, err := mobileDB(s.db).GetUserByEmail(r.Context(), to); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao verificar email"})
		return
	} else if user != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "email ja cadastrado"})
		return
	}

	mailer := email.NewService(s.cfg)
	if !mailer.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "SMTP nao configurado no backend. Defina SMTP_HOST, SMTP_PORT, SMTP_FROM_EMAIL, SMTP_USER e SMTP_PASS.",
		})
		return
	}

	if latest, err := mobileDB(s.db).LatestEmailOTP(r.Context(), to, registerEmailOTPPurpose); err == nil && latest != nil && latest.ConsumedAt == nil {
		if time.Since(latest.CreatedAt) < 30*time.Second {
			wait := int((30*time.Second - time.Since(latest.CreatedAt)).Seconds())
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":                "aguarde para reenviar o codigo",
				"resend_after_seconds": wait,
			})
			return
		}
	}

	code, err := randomNumericCode(6)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao gerar codigo"})
		return
	}
	expiresAt := time.Now().Add(10 * time.Minute)
	if err := mobileDB(s.db).CreateEmailOTP(r.Context(), to, registerEmailOTPPurpose, s.hashEmailOTP(to, code), expiresAt); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao salvar codigo"})
		return
	}

	if err := mailer.Send(email.Message{
		To:       to,
		Subject:  "ChainFX - codigo de verificacao",
		TextBody: "Seu codigo de verificacao ChainFX e: " + code + "\n\nEle expira em 10 minutos.",
		HTMLBody: buildRegisterOTPHTML(code),
	}); err != nil {
		slog.Warn("mobile register OTP email failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "falha ao enviar codigo por email"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"expires_in_seconds":   600,
		"resend_after_seconds": 30,
	})
}

func (s *Server) handleRegisterVerifyCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	to := strings.TrimSpace(strings.ToLower(req.Email))
	code := onlyDigitsMobile(req.Code)
	if to == "" || !strings.Contains(to, "@") || len(code) != 6 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "email ou codigo invalido"})
		return
	}
	otp, err := mobileDB(s.db).LatestEmailOTP(r.Context(), to, registerEmailOTPPurpose)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao validar codigo"})
		return
	}
	if otp == nil || otp.ConsumedAt != nil || time.Now().After(otp.ExpiresAt) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "codigo expirado ou invalido"})
		return
	}
	if otp.Attempts >= 5 {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "muitas tentativas. solicite um novo codigo"})
		return
	}
	if !hmac.Equal([]byte(otp.CodeHash), []byte(s.hashEmailOTP(to, code))) {
		_ = mobileDB(s.db).IncrementEmailOTPAttempts(r.Context(), otp.ID)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "codigo invalido"})
		return
	}
	if err := mobileDB(s.db).ConsumeEmailOTP(r.Context(), otp.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao confirmar codigo"})
		return
	}
	token, err := s.newEmailVerificationToken(to)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao emitir verificacao"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verified":                 true,
		"email_verification_token": token,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email                  string `json:"email"`
		Password               string `json:"password"`
		FullName               string `json:"full_name"`
		Phone                  string `json:"phone"`
		CPF                    string `json:"cpf"`
		BirthDate              string `json:"birth_date"`
		EmailVerificationToken string `json:"email_verification_token"`
		AddressPostalCode      string `json:"address_postal_code"`
		AddressStreet          string `json:"address_street"`
		AddressNumber          string `json:"address_number"`
		AddressNeighborhood    string `json:"address_neighborhood"`
		AddressCity            string `json:"address_city"`
		AddressState           string `json:"address_state"`
		AddressCountry         string `json:"address_country"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "email e password obrigatórios"})
		return
	}
	if req.EmailVerificationToken != "" || s.registerEmailOTPRequired() {
		if err := s.verifyEmailVerificationToken(req.Email, req.EmailVerificationToken); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "confirme o codigo enviado por email antes de criar a conta"})
			return
		}
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "password deve ter no mínimo 8 caracteres"})
		return
	}
	cost := bcrypt.DefaultCost
	if isMobileLoadTestUser(req.Email) {
		cost = bcrypt.MinCost
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), cost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	user, err := mobileDB(s.db).CreateUser(r.Context(), createMobileUserInput{
		Email:               req.Email,
		PasswordHash:        string(hash),
		FullName:            strings.TrimSpace(req.FullName),
		Phone:               strings.TrimSpace(req.Phone),
		CPF:                 onlyDigitsMobile(req.CPF),
		BirthDate:           strings.TrimSpace(req.BirthDate),
		AddressPostalCode:   onlyDigitsMobile(req.AddressPostalCode),
		AddressStreet:       strings.TrimSpace(req.AddressStreet),
		AddressNumber:       strings.TrimSpace(req.AddressNumber),
		AddressNeighborhood: strings.TrimSpace(req.AddressNeighborhood),
		AddressCity:         strings.TrimSpace(req.AddressCity),
		AddressState:        strings.ToUpper(strings.TrimSpace(req.AddressState)),
		AddressCountry:      firstNonEmptyStr(strings.ToUpper(strings.TrimSpace(req.AddressCountry)), "BR"),
	})
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "email já cadastrado"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar carteira do usuario"})
		return
	}
	access, _ := s.newAccessToken(user.ID)
	refresh, _ := s.newRefreshToken(user.ID)
	if err := mobileDB(s.db).SaveRefreshToken(r.Context(), user.ID, refresh); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar sessao"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":         s.sanitizeUser(user),
		"accessToken":  access,
		"refreshToken": refresh,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "email e password obrigatórios"})
		return
	}
	user, err := mobileDB(s.db).GetUserByEmail(r.Context(), req.Email)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "credenciais inválidas"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "credenciais inválidas"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar carteira do usuario"})
		return
	}
	access, _ := s.newAccessToken(user.ID)
	refresh, _ := s.newRefreshToken(user.ID)
	if err := mobileDB(s.db).SaveRefreshToken(r.Context(), user.ID, refresh); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar sessao"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         s.sanitizeUser(user),
		"accessToken":  access,
		"refreshToken": refresh,
	})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(r, &req); err != nil || req.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "refreshToken obrigatório"})
		return
	}
	claims, err := verifyToken(s.mcfg.RefreshSecret, req.RefreshToken)
	if err != nil || claims.Type != "refresh" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "refresh token inválido ou expirado"})
		return
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), claims.Sub)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "usuário não encontrado"})
		return
	}
	// C-05: validate refresh token against the stored server-side digest.
	// ensureUserWallet is intentionally NOT called here — token refresh only
	// needs to verify the session and issue new tokens; wallet state is
	// unchanged between requests and creating it here adds unnecessary latency.
	// Without this check a revoked token (after logout or password change)
	// remains valid for its full 7-day TTL — anyone with the token can still
	// obtain new access tokens even after the user has logged out.
	if user.RefreshTokenHash == nil || *user.RefreshTokenHash == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "sessão encerrada — faça login novamente"})
		return
	}
	if *user.RefreshTokenHash != refreshTokenDigest(req.RefreshToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "sessão inválida — faça login novamente"})
		return
	}
	access, _ := s.newAccessToken(user.ID)
	newRefresh, _ := s.newRefreshToken(user.ID)
	if err := mobileDB(s.db).SaveRefreshToken(r.Context(), user.ID, newRefresh); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao renovar sessao"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken":  access,
		"refreshToken": newRefresh,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	_ = mobileDB(s.db).ClearRefreshToken(r.Context(), uid)
	// Invalidate the cached active status so subsequent requests re-check the DB.
	s.invalidateUserActiveCache(uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func randomNumericCode(size int) (string, error) {
	max := big.NewInt(10)
	var b strings.Builder
	for b.Len() < size {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteString(strconv.FormatInt(n.Int64(), 10))
	}
	return b.String(), nil
}

func (s *Server) hashEmailOTP(emailAddress, code string) string {
	secret := ""
	if s != nil && s.mcfg != nil {
		secret = s.mcfg.JWTSecret
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strings.TrimSpace(strings.ToLower(emailAddress))))
	mac.Write([]byte(":"))
	mac.Write([]byte(code))
	return b64url(mac.Sum(nil))
}

func (s *Server) registerEmailOTPRequired() bool {
	configured := s != nil && s.cfg != nil && s.cfg.SMTPHost != "" && s.cfg.SMTPFromEmail != ""
	value := strings.TrimSpace(strings.ToLower(envOr("MOBILE_REGISTER_EMAIL_OTP_REQUIRED", "")))
	switch value {
	case "true", "1", "yes", "y", "on":
		return true
	case "false", "0", "no", "n", "off":
		return false
	default:
		return configured
	}
}

func (s *Server) verifyEmailVerificationToken(emailAddress, token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("missing token")
	}
	claims, err := verifyToken(s.mcfg.JWTSecret, token)
	if err != nil {
		return err
	}
	if claims.Type != "email_verification" {
		return fmt.Errorf("invalid token type")
	}
	if strings.TrimSpace(strings.ToLower(claims.Sub)) != strings.TrimSpace(strings.ToLower(emailAddress)) {
		return fmt.Errorf("email mismatch")
	}
	return nil
}

func buildRegisterOTPHTML(code string) string {
	return `<!doctype html><html><body style="margin:0;background:#111111;color:#f4f4f5;font-family:Arial,sans-serif;">
<div style="max-width:520px;margin:0 auto;padding:28px 20px;">
<h1 style="font-size:22px;margin:0 0 12px;">Codigo de verificacao</h1>
<p style="color:#a1a1aa;margin:0 0 20px;">Use este codigo para concluir seu cadastro na ChainFX.</p>
<div style="font-size:32px;letter-spacing:6px;font-weight:800;background:#1c1c1f;border:1px solid rgba(255,255,255,.12);border-radius:10px;padding:18px;text-align:center;">` + code + `</div>
<p style="color:#777c7f;font-size:13px;margin-top:20px;">Este codigo expira em 10 minutos. Se voce nao solicitou este cadastro, ignore este email.</p>
</div></body></html>`
}

func isMobileLoadTestUser(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	return strings.HasPrefix(email, "loadtest+") && strings.HasSuffix(email, "@chainfx.local")
}
