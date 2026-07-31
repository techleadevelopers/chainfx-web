package mobile

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"payment-gateway/internal/money"
)

var (
	errMobileTradeQuoteNotFound      = errors.New("quote_id invalido ou nao pertence ao usuario")
	errMobileTradeQuoteExpired       = errors.New("quote_id expirado")
	errMobileTradeQuoteConsumed      = errors.New("quote_id ja consumido")
	errMobileTradeQuoteMismatch      = errors.New("quote_id nao corresponde a operacao")
	errMobileTradeQuoteOwnerMismatch = errors.New("quote_id nao pertence ao usuario autenticado")
)

type mobileTradeQuoteRecord struct {
	QuoteID             string
	UserID              string
	Side                string
	Asset               string
	Network             string
	AmountMinor         int64
	AmountRaw           string
	RateMinor           int64
	FeeMinor            int64
	TotalMinor          int64
	ExpiresAt           time.Time
	ConsumedAt          sql.NullTime
	ConsumedOperationID sql.NullString
	IDempotencyKey      sql.NullString
	OrderID             sql.NullString
}

func (s *Server) issueMobileTradeQuote(ctx context.Context, userID string, claims mobileQuoteClaims) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", fmt.Errorf("usuario obrigatorio para quote")
	}
	quoteID := "mtq_" + randomTradeQuoteHex(24)
	claims.QuoteID = quoteID
	claims.UserID = userID
	claims.Side = strings.ToLower(strings.TrimSpace(claims.Side))
	claims.Asset = strings.ToUpper(strings.TrimSpace(claims.Asset))
	claims.Network = strings.ToUpper(strings.TrimSpace(claims.Network))
	if claims.ExpiresAt <= 0 {
		return "", fmt.Errorf("expires_at obrigatorio")
	}

	amountMinor, amountRaw := canonicalTradeAmountForAsset(claims.Side, claims.Asset, claims.Amount)
	if amountMinor <= 0 || amountRaw == "" {
		return "", fmt.Errorf("amount invalido")
	}
	token, err := s.issueMobileQuote(claims)
	if err != nil {
		return "", err
	}
	_, err = s.db.SQL.ExecContext(ctx, `
INSERT INTO mobile_trade_quotes
  (quote_id, user_id, side, asset, network, amount_minor, amount_raw,
   rate_minor, fee_minor, total_minor, expires_at)
VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		quoteID, userID, claims.Side, claims.Asset, claims.Network, amountMinor, amountRaw,
		int64(money.RateFromFloat(claims.Rate)), int64(money.MoneyFromFloat(claims.Fee)),
		int64(money.MoneyFromFloat(claims.Total)), time.Unix(claims.ExpiresAt, 0).UTC())
	if err != nil {
		return "", err
	}
	slog.Info("mobile_trade_quote_created", "quote_id", quoteID, "user_id", userID, "side", claims.Side, "asset", claims.Asset, "network", claims.Network)
	return token, nil
}

func (s *Server) consumeMobileTradeQuote(ctx context.Context, userID, rawQuoteID, side, asset, network string, amount float64, idempotencyKey string, now time.Time) (*mobileQuoteClaims, error) {
	claims, err := s.verifyMobileQuote(rawQuoteID, side, asset, amount, now, network)
	if err != nil {
		slog.Warn("mobile_trade_quote_expired_or_invalid", "user_id", userID, "side", side, "asset", asset, "network", network, "err", err)
		return nil, err
	}
	if claims.QuoteID == "" || claims.UserID == "" {
		return nil, errMobileTradeQuoteNotFound
	}
	if strings.TrimSpace(userID) == "" || !strings.EqualFold(claims.UserID, userID) {
		slog.Warn("mobile_trade_quote_owner_mismatch", "quote_id", claims.QuoteID, "authenticated_user", userID)
		return nil, errMobileTradeQuoteOwnerMismatch
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, fmt.Errorf("idempotency key obrigatorio")
	}

	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	record, err := txGetMobileTradeQuote(ctx, tx, claims.QuoteID)
	if err != nil {
		return nil, err
	}
	if record == nil || !strings.EqualFold(record.UserID, userID) {
		slog.Warn("mobile_trade_quote_owner_mismatch", "quote_id", claims.QuoteID, "authenticated_user", userID)
		return nil, errMobileTradeQuoteNotFound
	}
	if err := validateMobileTradeQuoteRecord(record, side, asset, network, amount); err != nil {
		return nil, err
	}
	if !record.ConsumedAt.Valid {
		if !now.UTC().Before(record.ExpiresAt.UTC()) {
			slog.Warn("mobile_trade_quote_expired", "quote_id", record.QuoteID, "user_id", userID)
			return nil, errMobileTradeQuoteExpired
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE mobile_trade_quotes
   SET consumed_at=NOW(), consumed_operation_id=$2, idempotency_key=$2
 WHERE quote_id=$1 AND consumed_at IS NULL`,
			record.QuoteID, idempotencyKey); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		slog.Info("mobile_trade_quote_consumed", "quote_id", record.QuoteID, "user_id", userID, "side", side)
		return claims, nil
	}

	if record.ConsumedOperationID.Valid && record.ConsumedOperationID.String == idempotencyKey {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		slog.Info("mobile_trade_quote_replay", "quote_id", record.QuoteID, "user_id", userID, "side", side)
		return claims, nil
	}
	slog.Warn("mobile_trade_quote_replay_rejected", "quote_id", record.QuoteID, "user_id", userID, "side", side)
	return nil, errMobileTradeQuoteConsumed
}

func (s *Server) attachMobileTradeQuoteOrder(ctx context.Context, quoteID, userID, orderID string) {
	if strings.TrimSpace(quoteID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(orderID) == "" {
		return
	}
	_, _ = s.db.SQL.ExecContext(ctx, `
UPDATE mobile_trade_quotes
   SET order_id=$3::uuid
 WHERE quote_id=$1 AND user_id=$2::uuid AND order_id IS NULL`,
		quoteID, userID, orderID)
}

func txGetMobileTradeQuote(ctx context.Context, tx *sql.Tx, quoteID string) (*mobileTradeQuoteRecord, error) {
	row := tx.QueryRowContext(ctx, `
SELECT quote_id, user_id::text, side, asset, network, amount_minor, amount_raw,
       rate_minor, fee_minor, total_minor, expires_at, consumed_at,
       consumed_operation_id, idempotency_key, order_id::text
  FROM mobile_trade_quotes
 WHERE quote_id=$1
 FOR UPDATE`, strings.TrimSpace(quoteID))
	record := &mobileTradeQuoteRecord{}
	err := row.Scan(&record.QuoteID, &record.UserID, &record.Side, &record.Asset, &record.Network,
		&record.AmountMinor, &record.AmountRaw, &record.RateMinor, &record.FeeMinor, &record.TotalMinor,
		&record.ExpiresAt, &record.ConsumedAt, &record.ConsumedOperationID, &record.IDempotencyKey, &record.OrderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

func validateMobileTradeQuoteRecord(record *mobileTradeQuoteRecord, side, asset, network string, amount float64) error {
	if record == nil {
		return errMobileTradeQuoteNotFound
	}
	side = strings.ToLower(strings.TrimSpace(side))
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = strings.ToUpper(strings.TrimSpace(network))
	if !strings.EqualFold(record.Side, side) || !strings.EqualFold(record.Asset, asset) {
		return errMobileTradeQuoteMismatch
	}
	if strings.TrimSpace(record.Network) != "" && !strings.EqualFold(record.Network, network) {
		return errMobileTradeQuoteMismatch
	}
	amountMinor, amountRaw := canonicalTradeAmountForAsset(side, asset, amount)
	if record.AmountMinor != amountMinor || strings.TrimSpace(record.AmountRaw) != amountRaw {
		return errMobileTradeQuoteMismatch
	}
	return nil
}

func canonicalTradeAmount(side string, amount float64) (int64, string) {
	return canonicalTradeAmountForAsset(side, "", amount)
}

func canonicalTradeAmountForAsset(side, asset string, amount float64) (int64, string) {
	if strings.EqualFold(side, "buy") {
		minor := int64(money.MoneyFromFloat(amount))
		return minor, fmt.Sprintf("%d", minor)
	}
	if strings.EqualFold(asset, "BTC") {
		sats := btcToSats(amount)
		return sats, fmt.Sprintf("%d", sats)
	}
	units := int64(money.TokenFromFloat(amount))
	return units, fmt.Sprintf("%d", units)
}

func btcToSats(amountBTC float64) int64 {
	if amountBTC <= 0 {
		return 0
	}
	return int64(amountBTC*100000000 + 0.5)
}

func satsToBTCFloat(sats int64) float64 {
	return float64(sats) / 100000000
}

func randomTradeQuoteHex(n int) string {
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
