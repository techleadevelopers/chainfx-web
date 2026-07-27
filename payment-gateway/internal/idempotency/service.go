package idempotency

import (
	"context"

	"payment-gateway/internal/database"
)

type Store interface {
	BeginIdempotency(context.Context, string, string, string, string) (*database.IdempotencyStart, error)
	CompleteIdempotency(context.Context, string, string, string, string, string, int, any) error
	FailIdempotency(context.Context, string, string, string, string, string, int, any) error
}

type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Begin(ctx context.Context, key, operation, apiKeyHash, bodyHash string) (*database.IdempotencyStart, error) {
	return s.store.BeginIdempotency(ctx, key, operation, apiKeyHash, bodyHash)
}

func (s *Service) Complete(ctx context.Context, key, operation, apiKeyHash, resultType, resultID string, responseStatus int, response any) error {
	return s.store.CompleteIdempotency(ctx, key, operation, apiKeyHash, resultType, resultID, responseStatus, response)
}

func (s *Service) Fail(ctx context.Context, key, operation, apiKeyHash, resultType, resultID string, responseStatus int, response any) error {
	return s.store.FailIdempotency(ctx, key, operation, apiKeyHash, resultType, resultID, responseStatus, response)
}
