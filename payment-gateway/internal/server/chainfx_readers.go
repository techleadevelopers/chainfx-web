package server

import "context"

func (s *Server) readChainFXBuy(ctx context.Context, id, accessToken string) (map[string]any, bool) {
	ok, err := s.db.ValidateBuyAccess(ctx, id, accessToken)
	if err != nil || !ok {
		return nil, false
	}
	buy, err := s.db.GetBuyOrder(ctx, id)
	if err != nil || buy == nil {
		return nil, false
	}
	return map[string]any{
		"id":           buy.ID,
		"side":         "buy",
		"status":       buy.Status,
		"fiat":         buy.FiatCurrency,
		"asset":        buy.Asset,
		"amountFiat":   buy.AmountFiat,
		"feeFiat":      buy.FeeBRL,
		"totalFiat":    buy.AmountFiat,
		"payoutFiat":   buy.PayoutBRL,
		"cryptoAmount": buy.CryptoAmount,
		"rate":         buy.RateLocked,
		"wallet":       buy.DestAddress,
		"paymentRail":  buy.PaymentMethod,
		"payment":      jsonRawToMap(buy.PixPayload),
		"txHash":       buy.TxHashOut,
		"error":        buy.Error,
		"createdAt":    buy.CreatedAt,
		"updatedAt":    buy.UpdatedAt,
		"expiresAt":    buy.RateLockExpiresAt,
	}, true
}

func (s *Server) readChainFXSell(ctx context.Context, id, accessToken string) (map[string]any, bool) {
	ok, err := s.db.ValidateOrderAccess(ctx, id, accessToken)
	if err != nil || !ok {
		return nil, false
	}
	order, err := s.db.GetOrder(ctx, id)
	if err != nil || order == nil {
		return nil, false
	}
	return map[string]any{
		"id":             order.ID,
		"side":           "sell",
		"status":         order.Status,
		"fiat":           "BRL",
		"asset":          order.Asset,
		"network":        order.Network,
		"amountFiat":     order.AmountBRL,
		"feeFiat":        order.FeeBRL,
		"payoutFiat":     order.PayoutBRL,
		"cryptoAmount":   order.AmountUSDT,
		"rate":           order.RateLocked,
		"depositAddress": order.Address,
		"pixKey":         order.PixKey,
		"txHash":         order.TxHash,
		"depositTx":      order.DepositTx,
		"depositAmount":  order.DepositAmount,
		"error":          order.Error,
		"createdAt":      order.CreatedAt,
		"updatedAt":      order.UpdatedAt,
		"expiresAt":      order.RateLockExpiresAt,
	}, true
}
