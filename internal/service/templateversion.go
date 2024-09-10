package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-openapi/swag"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/labels"
)

func TemplateVersionFromReader(r io.Reader) (*api.TemplateVersion, error) {
	var templateVersion api.TemplateVersion
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&templateVersion)
	return &templateVersion, err
}

// (GET /api/v1/api/v1/fleets/{fleet}/templateVersions)
func (h *ServiceHandler) ListTemplateVersions(ctx context.Context, request server.ListTemplateVersionsRequestObject) (server.ListTemplateVersionsResponseObject, error) {
	orgId := store.NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return server.ListTemplateVersions400JSONResponse{Message: err.Error()}, nil
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	listParams := store.ListParams{
		Labels:    labelMap,
		Limit:     int(swag.Int32Value(request.Params.Limit)),
		Continue:  cont,
		FleetName: &request.Fleet,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListTemplateVersions400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.TemplateVersion().List(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListTemplateVersions200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/api/v1/fleets/{fleet}/templateVersions)
func (h *ServiceHandler) DeleteTemplateVersions(ctx context.Context, request server.DeleteTemplateVersionsRequestObject) (server.DeleteTemplateVersionsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.TemplateVersion().DeleteAll(ctx, orgId, &request.Fleet)
	switch err {
	case nil:
		return server.DeleteTemplateVersions200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *ServiceHandler) ReadTemplateVersion(ctx context.Context, request server.ReadTemplateVersionRequestObject) (server.ReadTemplateVersionResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.TemplateVersion().Get(ctx, orgId, request.Fleet, request.Name)
	switch err {
	case nil:
		return server.ReadTemplateVersion200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets/{fleet}/templateVersions/{name})
func (h *ServiceHandler) DeleteTemplateVersion(ctx context.Context, request server.DeleteTemplateVersionRequestObject) (server.DeleteTemplateVersionResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.TemplateVersion().Delete(ctx, orgId, request.Fleet, request.Name)
	switch err {
	case nil:
		return server.DeleteTemplateVersion200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DeleteTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}

func approveTemplateVersion(templateVersion *api.TemplateVersion, approval *api.TemplateVersionApproval) error {
	if templateVersion == nil {
		return errors.New("approveTemplateVersion: templateVersion is nil")
	}

	if templateVersion.Metadata.Name == nil {
		return fmt.Errorf("approveTemplateVersion: missing metadata.name")
	}

	templateVersion.Status = &api.TemplateVersionStatus{
		Conditions: []api.Condition{},
		Approval:   approval,
	}

	condition := api.Condition{
		Type:    api.TemplateVersionApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "ManuallyApproved",
		Message: "Approved by " + *approval.ApprovedBy,
	}
	api.SetStatusCondition(&templateVersion.Status.Conditions, condition)
	return nil

}

func rejectTemplateVersion(templateVersion *api.TemplateVersion, approval *api.TemplateVersionApproval) error {
	if templateVersion == nil {
		return errors.New("rejectTemplateVersion: templateVersion is nil")
	}

	if templateVersion.Metadata.Name == nil {
		return fmt.Errorf("approveAndSignEnrollmentRequest: missing metadata.name")
	}

	templateVersion.Status = &api.TemplateVersionStatus{
		Conditions: []api.Condition{},
		Approval:   approval,
	}

	condition := api.Condition{
		Type:    api.TemplateVersionApproved,
		Status:  api.ConditionStatusFalse,
		Reason:  "ManuallyRejected",
		Message: "Rejected by " + *approval.ApprovedBy,
	}
	api.SetStatusCondition(&templateVersion.Status.Conditions, condition)
	return nil

}

// (POST /api/v1/fleets/{fleet}/templateVersions/{name}/approval)
func (h *ServiceHandler) ApproveTemplateVersion(ctx context.Context, request server.ApproveTemplateVersionRequestObject) (server.ApproveTemplateVersionResponseObject, error) {
	orgId := store.NullOrgId

	templateVersion, err := h.store.TemplateVersion().Get(ctx, orgId, request.Fleet, request.Name)
	switch err {
	default:
		return nil, err
	case flterrors.ErrResourceNotFound:
		return server.ApproveTemplateVersion404JSONResponse{}, nil
	case nil:
	}

	// The same check should happen for ApprovedBy, but we don't have a way to identify
	// users yet, so we'll let the UI set it for now.
	if request.Body.ApprovedBy == nil {
		request.Body.ApprovedBy = util.StrToPtr("unknown")
	}

	if api.IsStatusConditionTrue(templateVersion.Status.Conditions, api.TemplateVersionApproved) {
		return server.ApproveTemplateVersion400JSONResponse{Message: "Template version is already approved"}, nil
	}
	if request.Body.ApprovedAt != nil {
		return server.ApproveTemplateVersion400JSONResponse{Message: "ApprovedAt is not allowed to be set when approving template version"}, nil
	}

	// if the enrollment request was already approved we should not try to approve it one more time
	if request.Body.Approved {
		request.Body.ApprovedAt = util.TimeToPtr(time.Now())

		if err := approveTemplateVersion(templateVersion, request.Body); err != nil {
			return server.ApproveTemplateVersion400JSONResponse{Message: fmt.Sprintf("Error approving and template version: %v", err.Error())}, nil
		}
	} else {
		if err := rejectTemplateVersion(templateVersion, request.Body); err != nil {
			return server.ApproveTemplateVersion400JSONResponse{Message: fmt.Sprintf("Error rejecting and template version: %v", err.Error())}, nil
		}

	}
	var tv *model.TemplateVersion
	err = h.store.TemplateVersion().UpdateStatus(ctx, orgId, templateVersion, nil, func(t *model.TemplateVersion) { tv = t })
	switch err {
	case nil:
		var approval api.TemplateVersionApproval
		if tv != nil && tv.Status != nil {
			approval = lo.FromPtr(tv.Status.Data.Approval)
		}
		return server.ApproveTemplateVersion200JSONResponse{
			Approved:   approval.Approved,
			ApprovedAt: approval.ApprovedAt,
			ApprovedBy: approval.ApprovedBy,
		}, nil
	case flterrors.ErrResourceNotFound:
		return server.ApproveTemplateVersion404JSONResponse{}, nil
	default:
		return nil, err
	}
}
