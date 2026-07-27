package email

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/smtp"
	"net/textproto"
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

	addr := fmt.Sprintf("%s:%d", s.cfg.SMTPHost, s.cfg.SMTPPort)
	auth := smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPass, s.cfg.SMTPHost)
	if s.cfg.SMTPSecure {
		return s.sendStartTLS(addr, auth, raw, msg.To)
	}
	return smtp.SendMail(addr, auth, s.cfg.SMTPFromEmail, []string{msg.To}, raw)
}

func (s *Service) NotifyOps(subject, body string) {
	if s.cfg.OpsEmail == "" {
		return
	}
	if err := s.Send(Message{To: s.cfg.OpsEmail, Subject: subject, Body: body}); err != nil {
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

func (s *Service) brand() Brand {
	return Brand{
		Name:         firstNonEmpty(s.cfg.EmailBrandName, "ChainFX"),
		LogoURL:      firstNonEmpty(s.cfg.EmailLogoURL, "https://www.chainfx.store/logo.png"),
		SiteURL:      firstNonEmpty(s.cfg.EmailSiteURL, "https://www.chainfx.store"),
		Address:      firstNonEmpty(s.cfg.EmailAddress, "ChainFX Payments"),
		SupportEmail: firstNonEmpty(s.cfg.SupportEmail, s.cfg.SMTPFromEmail),
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

func (s *Service) sendStartTLS(addr string, auth smtp.Auth, raw []byte, to string) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
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
