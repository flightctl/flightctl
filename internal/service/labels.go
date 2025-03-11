package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
)

// (GET /api/v1/labels)
func (h *ServiceHandler) ListLabels(ctx context.Context, params api.ListLabelsParams) (*api.LabelList, api.Status) {
	orgId := store.NullOrgId
	kind := params.Kind

	var (
		fieldSelector *selector.FieldSelector
		labelSelector *selector.LabelSelector
		err           error
	)

	if params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*params.FieldSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))
		}
	}

	if params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*params.LabelSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))
		}
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(params.Limit)),
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	} else if listParams.Limit > store.MaxRecordsPerListRequest {
		return nil, api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest))
	} else if listParams.Limit < 0 {
		return nil, api.StatusBadRequest("limit cannot be negative")
	}

	// Retrieve labels based on the resource kind
	var result api.LabelList
	switch kind {
	case api.DeviceKind:
		result, err = h.store.Device().Labels(ctx, orgId, listParams)
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
