package pricing

import (
	"payment-gateway/internal/money"
)

type Service struct {
	FeeBps int
}

func New(feeBps int) *Service {
	return &Service{FeeBps: feeBps}
}

func (s *Service) Fee(amount money.MoneyMinor) money.MoneyMinor {
	return money.FeeBps(amount, s.FeeBps)
}

func (s *Service) BuyRate(market money.RateDecimal, spreadBps int) money.RateDecimal {
	return money.AddBps(market, spreadBps)
}

func (s *Service) SellRate(market money.RateDecimal, spreadBps int) money.RateDecimal {
	return money.SubtractBps(market, spreadBps)
}
