package appinspection

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	appinspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/google/uuid"
)

const (
	resultRetention  = 7 * 24 * time.Hour
	cleanupBatchSize = 1000
)

type Cleaner struct {
	store *appinspectionstore.Store
}

func NewCleaner(store *appinspectionstore.Store) (*Cleaner, error) {
	if store == nil {
		return nil, errors.New("application inspection store is required")
	}
	return &Cleaner{store: store}, nil
}

func (c *Cleaner) Cleanup(ctx context.Context, orgID uuid.UUID, now time.Time) (int64, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service/appinspection", "Cleanup")
	defer span.End()
	return c.store.DeleteExpiredUnreferenced(ctx, orgID, now.Add(-resultRetention), cleanupBatchSize)
}
