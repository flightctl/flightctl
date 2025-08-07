package service

import (
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/samber/lo"
)

func prepareListParams(cont *string, lSelector *string, fSelector *string, limit *int32) (*store.ListParams, api.Status) {
	cnt, err := store.ParseContinueString(cont)
	if err != nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))
	}

	var fieldSelector *selector.FieldSelector
	if fSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*fSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))
		}
	}

	var labelSelector *selector.LabelSelector
	if lSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*lSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))
		}
	}

	listParams := &store.ListParams{
		Limit:         int(lo.FromPtr(limit)),
		Continue:      cnt,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	} else if listParams.Limit > MaxRecordsPerListRequest {
		return nil, api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", MaxRecordsPerListRequest))
	} else if listParams.Limit < 0 {
		return nil, api.StatusBadRequest("limit cannot be negative")
	}

	return listParams, api.StatusOK()
}
