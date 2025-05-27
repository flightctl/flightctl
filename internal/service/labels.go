package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
)

// (GET /api/v1/labels)
func (h *ServiceHandler) ListLabels(ctx context.Context, params api.ListLabelsParams) (*api.LabelList, api.Status) {
	var err error

	orgId := store.NullOrgId
	kind := params.Kind

	listParams, status := prepareListParams(nil, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	// Retrieve labels based on the resource kind
	var result api.LabelList
	switch kind {
	case api.DeviceKind:
		result, err = h.store.Device().Labels(ctx, orgId, *listParams)
	default:
		return nil, api.StatusBadRequest(fmt.Sprintf("unsupported kind: %s", kind))
	}

	if err == nil {
		return &result, api.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, api.StatusBadRequest(se.Error())
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}
