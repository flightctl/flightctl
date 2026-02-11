package service

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
)

// (GET /api/v1/labels)
func (h *ServiceHandler) ListLabels(ctx context.Context, orgId uuid.UUID, params domain.ListLabelsParams) (*domain.LabelList, domain.Status) {
	var err error

	kind := params.Kind

	listParams, status := prepareListParams(nil, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	// Retrieve labels based on the resource kind
	var result domain.LabelList
	switch kind {
	case domain.DeviceKind:
		result, err = h.store.Device().Labels(ctx, orgId, *listParams)
	default:
		return nil, domain.StatusBadRequest(fmt.Sprintf("unsupported kind: %s", kind))
	}

	if err == nil {
		return &result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}
