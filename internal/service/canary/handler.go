package canary

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/service/common"
	canarystore "github.com/flightctl/flightctl/internal/store/canary"
	"github.com/flightctl/flightctl/internal/store/model"
)

const CanaryKind = "EncryptionCanary"

type ServiceHandler struct {
	store canarystore.Store
}

func NewServiceHandler(store canarystore.Store) *ServiceHandler {
	return &ServiceHandler{store: store}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) Get(ctx context.Context, strategy, keyID string) (*encryption.Canary, domain.Status) {
	row, err := h.store.Get(ctx, strategy, keyID)
	status := common.StoreErrorToApiStatus(err, false, CanaryKind, nil)
	if err != nil {
		return nil, status
	}
	return row.ToEncryptionCanary(), status
}

func (h *ServiceHandler) Save(ctx context.Context, canary *encryption.Canary) domain.Status {
	err := h.store.CreateOrUpdate(ctx, model.EncryptionCanaryFrom(canary))
	return common.StoreErrorToApiStatus(err, false, CanaryKind, nil)
}

func (h *ServiceHandler) GetAll(ctx context.Context) ([]encryption.Canary, domain.Status) {
	rows, err := h.store.List(ctx)
	status := common.StoreErrorToApiStatus(err, false, CanaryKind, nil)
	if err != nil {
		return nil, status
	}
	result := make([]encryption.Canary, len(rows))
	for i := range rows {
		result[i] = *rows[i].ToEncryptionCanary()
	}
	return result, status
}

func (h *ServiceHandler) PrepareForRetirement(ctx context.Context, strategy, keyID string) domain.Status {
	_, err := h.store.Delete(ctx, strategy, keyID)
	return common.StoreErrorToApiStatus(err, false, CanaryKind, nil)
}
