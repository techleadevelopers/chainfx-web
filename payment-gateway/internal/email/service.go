package email

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"

	"payment-gateway/internal/config"
)

type Service struct {
	cfg *config.Config
}

type Message struct {
	To       string
	Subject  string
	Body     string
	TextBody string
	HTMLBody string
}

func NewService(cfg *config.Config) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) Enabled() bool {
	if s == nil || s.cfg == nil {
		return false
	}
	if s.shouldUseBrevoAPI() {
		return s.brevoSenderEmail() != "" && s.brevoAPIKey() != ""
	}
	return s.cfg.SMTPHost != "" && s.cfg.SMTPPort > 0 && s.cfg.SMTPFromEmail != ""
}

func (s *Service) Send(msg Message) error {
	if !s.Enabled() {
		slog.Info("Email desabilitado: SMTP não configurado", "subject", msg.Subject)
		return nil
	}
	if msg.To == "" {
		return fmt.Errorf("destinatário de email vazio")
	}

	fromName := strings.TrimSpace(s.cfg.SMTPFromName)
	from := s.cfg.SMTPFromEmail
	if fromName != "" {
		from = fmt.Sprintf("%s <%s>", fromName, s.cfg.SMTPFromEmail)
	}

	raw := s.renderMIME(from, msg)
	if s.shouldUseBrevoAPI() {
		return s.sendBrevoAPI(fromName, msg)
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.SMTPHost, s.cfg.SMTPPort)
	auth := smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPass, s.cfg.SMTPHost)
	return s.sendSMTP(addr, auth, raw, msg.To, s.cfg.SMTPSecure)
}

func (s *Service) shouldUseBrevoAPI() bool {
	if strings.TrimSpace(os.Getenv("BREVO_API_KEY")) != "" {
		return true
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(s.cfg.SMTPHost)), "brevo") &&
		strings.HasPrefix(strings.TrimSpace(s.cfg.SMTPPass), "xkeysib-")
}

func (s *Service) brevoAPIKey() string {
	return firstNonEmpty(os.Getenv("BREVO_API_KEY"), s.cfg.SMTPPass)
}

func (s *Service) brevoSenderEmail() string {
	user := strings.TrimSpace(s.cfg.SMTPUser)
	if strings.Contains(strings.ToLower(user), "@smtp-brevo.com") {
		return user
	}
	return firstNonEmpty(s.cfg.SMTPFromEmail, user)
}

func (s *Service) sendBrevoAPI(fromName string, msg Message) error {
	apiKey := s.brevoAPIKey()
	if apiKey == "" {
		return fmt.Errorf("brevo api key vazia")
	}
	textBody := firstNonEmpty(msg.TextBody, msg.Body)
	payload := map[string]any{
		"sender": map[string]string{
			"email": s.brevoSenderEmail(),
			"name":  firstNonEmpty(fromName, s.cfg.SMTPFromName, "ChainFX"),
		},
		"to": []map[string]string{
			{"email": msg.To},
		},
		"subject": msg.Subject,
	}
	if html := strings.TrimSpace(msg.HTMLBody); html != "" {
		payload["htmlContent"] = html
	} else {
		payload["textContent"] = textBody
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, firstNonEmpty(os.Getenv("BREVO_API_URL"), "https://api.brevo.com/v3/smtp/email"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", apiKey)
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	client := &http.Client{Timeout: 12 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	rawResponse, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("brevo email api status %d: %s", res.StatusCode, strings.TrimSpace(string(rawResponse)))
	}
	var response struct {
		MessageID string `json:"messageId"`
	}
	if err := json.Unmarshal(rawResponse, &response); err == nil && strings.TrimSpace(response.MessageID) != "" {
		slog.Info("brevo transactional email accepted", "message_id", response.MessageID)
	}
	return nil
}

func (s *Service) NotifyOps(subject, body string) {
	if s.cfg.OpsEmail == "" {
		return
	}
	if err := s.Send(BuildOpsMessage(s.cfg.OpsEmail, subject, body, s.brand())); err != nil {
		slog.Warn("Falha ao enviar email operacional", "error", err)
	}
}

func (s *Service) NotifyOpsOrderCreated(receipt OpsOrderCreated) {
	if s.cfg.OpsEmail == "" {
		return
	}
	receipt.Brand = s.brand()
	if err := s.Send(BuildOpsOrderCreatedMessage(s.cfg.OpsEmail, receipt)); err != nil {
		slog.Warn("Falha ao enviar email operacional", "error", err)
	}
}

func (s *Service) SendBuyCompleted(to string, receipt Receipt) error {
	receipt.Kind = "buy"
	receipt.Brand = s.brand()
	return s.Send(BuildReceiptMessage(to, "Compra concluida na ChainFX", receipt))
}

func (s *Service) SendSellCompleted(to string, receipt Receipt) error {
	receipt.Kind = "sell"
	receipt.Brand = s.brand()
	return s.Send(BuildReceiptMessage(to, "Venda concluida na ChainFX", receipt))
}

func (s *Service) SendMarketing(to string, campaign MarketingCampaign) error {
	campaign.Brand = s.brand()
	return s.Send(BuildMarketingMessage(to, campaign))
}

func (s *Service) SendTransaction(to, subject string, receipt TransactionReceipt) error {
	receipt.Brand = s.brand()
	return s.Send(BuildTransactionMessage(to, subject, receipt))
}

func (s *Service) brand() Brand {
	return Brand{
		Name:         firstNonEmpty(s.cfg.EmailBrandName, "ChainFX"),
		LogoURL:      firstNonEmpty(s.cfg.EmailLogoURL, "https://www.chainfx.store/logo.png"),
		SiteURL:      firstNonEmpty(s.cfg.EmailSiteURL, "https://www.chainfx.store"),
		Address:      firstNonEmpty(s.cfg.EmailAddress, "ChainFX Payments"),
		SupportEmail: firstNonEmpty(s.cfg.SupportEmail, "support@chainfx.store"),
		Year:         time.Now().Year(),
	}
}

func (s *Service) renderMIME(from string, msg Message) []byte {
	textBody := firstNonEmpty(msg.TextBody, msg.Body)
	htmlBody := strings.TrimSpace(msg.HTMLBody)
	headers := []string{
		"From: " + from,
		"To: " + msg.To,
		"Subject: " + sanitizeHeader(msg.Subject),
		"MIME-Version: 1.0",
	}
	if htmlBody == "" {
		headers = append(headers, "Content-Type: text/plain; charset=UTF-8")
		return []byte(strings.Join(append(headers, "", textBody), "\r\n"))
	}
	boundary := fmt.Sprintf("chainfx-%d", time.Now().UnixNano())
	headers = append(headers, "Content-Type: multipart/alternative; boundary="+boundary)
	parts := []string{
		strings.Join(headers, "\r\n"),
		"",
		"--" + boundary,
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		textBody,
		"--" + boundary,
		"Content-Type: text/html; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		htmlBody,
		"--" + boundary + "--",
		"",
	}
	return []byte(strings.Join(parts, "\r\n"))
}

func (s *Service) sendSMTP(addr string, auth smtp.Auth, raw []byte, to string, startTLS bool) error {
	conn, err := net.DialTimeout("tcp", addr, 8*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))

	client, err := smtp.NewClient(conn, s.cfg.SMTPHost)
	if err != nil {
		return err
	}
	defer client.Close()
	if startTLS {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("smtp STARTTLS indisponivel")
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.cfg.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(s.cfg.SMTPFromEmail); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

func sanitizeHeader(value string) string {
	return textproto.TrimString(strings.NewReplacer("\r", "", "\n", "").Replace(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
