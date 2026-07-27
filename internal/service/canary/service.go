package canary

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
)

// Service is the entry point for canary operations.
type Service interface {
	Get(ctx context.Context, strategy, keyID string) (*encryption.Canary, domain.Status)
	Save(ctx context.Context, canary *encryption.Canary) domain.Status
	GetAll(ctx context.Context) ([]encryption.Canary, domain.Status)
	PrepareForRetirement(ctx context.Context, strategy, keyID string) domain.Status
}
