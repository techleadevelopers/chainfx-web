package mobile

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"payment-gateway/internal/email"
)

func (s *Server) sendMobileTransactionEmailAsync(userID, subject string, receipt email.TransactionReceipt) {
	if s == nil || s.cfg == nil || s.db == nil || strings.TrimSpace(userID) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		to, err := s.mobileUserEmail(ctx, userID)
		if err != nil || strings.TrimSpace(to) == "" {
			slog.Info("mobile email sem destinatario", "user_id", userID, "subject", subject, "error", err)
			return
		}
		s.sendMobileTransactionEmailToAsync(to, subject, receipt)
	}()
}

func (s *Server) sendMobileTransactionEmailToAsync(to, subject string, receipt email.TransactionReceipt) {
	if s == nil || s.cfg == nil || strings.TrimSpace(to) == "" {
		return
	}
	go func() {
		mailer := email.NewService(s.cfg)
		if !mailer.Enabled() {
			return
		}
		if err := mailer.SendTransaction(to, subject, receipt); err != nil {
			slog.Warn("mobile email failed", "to_domain", emailDomainForLog(to), "subject", subject, "error", err)
		}
	}()
}

func (s *Server) sendMobileSecurityEmailAsync(userID, title, intro string, details ...email.TransactionDetail) {
	details = append(details, email.TransactionDetail{Label: "Quando", Value: mobileNowText()})
	s.sendMobileTransactionEmailAsync(userID, "Seguranca ChainFX: "+title, email.TransactionReceipt{
		Title:   title,
		Intro:   intro,
		CTA:     "Abrir app",
		Details: details,
	})
}

func (s *Server) mobileUserEmail(ctx context.Context, userID string) (string, error) {
	if s == nil || s.db == nil || s.db.SQL == nil {
		return "", sql.ErrConnDone
	}
	var to string
	err := s.db.SQL.QueryRowContext(ctx, `SELECT email FROM users WHERE id=$1::uuid`, userID).Scan(&to)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(to), nil
}

func mobileScanURL(network, txHash string) string {
	txHash = strings.TrimSpace(txHash)
	if txHash == "" {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "BSC", "BEP20", "BNB", "BINANCE":
		return "https://bscscan.com/tx/" + txHash
	case "POLYGON", "MATIC":
		return "https://polygonscan.com/tx/" + txHash
	case "ETH", "ETHEREUM", "ERC20":
		return "https://etherscan.io/tx/" + txHash
	case "BASE":
		return "https://basescan.org/tx/" + txHash
	case "ARBITRUM", "ARB":
		return "https://arbiscan.io/tx/" + txHash
	case "BTC", "BITCOIN":
		return "https://mempool.space/tx/" + txHash
	case "SOL", "SOLANA":
		return "https://solscan.io/tx/" + txHash
	default:
		return ""
	}
}

func mobileMoneyBRL(v float64) string {
	return fmt.Sprintf("R$ %.2f", v)
}

func mobileNowText() string {
	return time.Now().Format("02/01/2006 15:04 MST")
}
