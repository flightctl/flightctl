package checkpoint

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
)

type Service interface {
	GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, domain.Status)
	SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) domain.Status
	GetDatabaseTime(ctx context.Context) (time.Time, domain.Status)
}
