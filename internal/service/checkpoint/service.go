package checkpoint

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
)

// Service is the focused Checkpoint service interface, extracted from the monolithic
// internal/service.Service (internal/service/checkpoint.go).
type Service interface {
	GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, domain.Status)
	SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) domain.Status
	GetDatabaseTime(ctx context.Context) (time.Time, domain.Status)
}
