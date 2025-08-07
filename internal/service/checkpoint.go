package service

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/samber/lo"
)

const CheckpointKind = "Checkpoint"

func (h *ServiceHandler) GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, api.Status) {
	s, err := h.store.Checkpoint().Get(ctx, consumer, key)
	status := StoreErrorToApiStatus(err, false, CheckpointKind, lo.ToPtr(fmt.Sprintf("%s/%s", consumer, key)))
	return s, status
}

func (h *ServiceHandler) SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) api.Status {
	err := h.store.Checkpoint().Set(ctx, consumer, key, value)
	status := StoreErrorToApiStatus(err, false, CheckpointKind, lo.ToPtr(fmt.Sprintf("%s/%s", consumer, key)))
	return status
}

func (h *ServiceHandler) GetDatabaseTime(ctx context.Context) (time.Time, api.Status) {
	dbTime, err := h.store.Checkpoint().GetDatabaseTime(ctx)
	status := StoreErrorToApiStatus(err, false, CheckpointKind, nil)
	return dbTime, status
}
