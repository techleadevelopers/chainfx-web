package quotes

import (
	"context"

	"payment-gateway/internal/database"
)

type Store interface {
	CreateQuote(context.Context, database.QuoteInput) (*database.Quote, error)
	ConsumeQuote(context.Context, database.QuoteConsumeInput) (*database.Quote, error)
	GetQuote(context.Context, string) (*database.Quote, error)
}

type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Create(ctx context.Context, in database.QuoteInput) (*database.Quote, error) {
	return s.store.CreateQuote(ctx, in)
}

func (s *Service) Consume(ctx context.Context, in database.QuoteConsumeInput) (*database.Quote, error) {
	return s.store.ConsumeQuote(ctx, in)
}

func (s *Service) Get(ctx context.Context, id string) (*database.Quote, error) {
	return s.store.GetQuote(ctx, id)
}
