package service

import (
	"context"
	"errors"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
)

// (GET /api/v1/labels)
func (h *ServiceHandler) ListLabels(ctx context.Context, request server.ListLabelsRequestObject) (server.ListLabelsResponseObject, error) {
	orgId := store.NullOrgId
	kind := request.Params.Kind

	var (
		fieldSelector *selector.FieldSelector
		labelSelector *selector.LabelSelector
		err           error
	)

	if request.Params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*request.Params.FieldSelector); err != nil {
			return server.ListLabels400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
		}
	}

	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListLabels400JSONResponse{Message: fmt.Sprintf("failed to parse label selector: %v", err)}, nil
		}
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListLabels400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	// Retrieve labels based on the resource kind
	var result api.LabelList
	switch kind {
	case "Device":
		result, err = h.store.Device().Labels(ctx, orgId, listParams)
	default:
		return server.ListLabels400JSONResponse{Message: fmt.Sprintf("unsupported kind: %s", kind)}, nil
	}

	if err == nil {
		return server.ListLabels200JSONResponse(result), nil
	}

	var se *selector.SelectorError

	switch {
	case errors.Is(err, flterrors.ErrLimitParamOutOfBounds):
		return server.ListLabels400JSONResponse{Message: err.Error()}, nil
	case selector.AsSelectorError(err, &se):
		return server.ListLabels400JSONResponse{Message: se.Error()}, nil
	default:
		return nil, err
	}
}
