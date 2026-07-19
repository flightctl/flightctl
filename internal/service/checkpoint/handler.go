package checkpoint

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
	"github.com/samber/lo"
)

const CheckpointKind = "Checkpoint"

type ServiceHandler struct {
	store checkpointstore.Store
}

// NewServiceHandler creates a new checkpoint ServiceHandler instance.
func NewServiceHandler(store checkpointstore.Store) *ServiceHandler {
	return &ServiceHandler{store: store}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, domain.Status) {
	s, err := h.store.Get(ctx, consumer, key)
	status := common.StoreErrorToApiStatus(err, false, CheckpointKind, lo.ToPtr(fmt.Sprintf("%s/%s", consumer, key)))
	return s, status
}

func (h *ServiceHandler) SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) domain.Status {
	err := h.store.Set(ctx, consumer, key, value)
	return common.StoreErrorToApiStatus(err, false, CheckpointKind, lo.ToPtr(fmt.Sprintf("%s/%s", consumer, key)))
}

func (h *ServiceHandler) GetDatabaseTime(ctx context.Context) (time.Time, domain.Status) {
	dbTime, err := h.store.GetDatabaseTime(ctx)
	status := common.StoreErrorToApiStatus(err, false, CheckpointKind, nil)
	return dbTime, status
}
