package labelsyncmapping

import (
	"context"
	"errors"
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	eventservice "github.com/flightctl/flightctl/internal/service/events"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store       labelsyncmappingstore.Store
	deviceStore ReconciliationDeviceStore
	evaluator   Evaluator
	events      eventservice.Service
	log         logrus.FieldLogger
}

// NewServiceHandler constructs the label-sync mapping service, including CEL
// validation for CRUD operations and device-label reconciliation. It returns
// an error if any required dependency is nil.
func NewServiceHandler(store labelsyncmappingstore.Store, deviceStore ReconciliationDeviceStore, evaluator Evaluator, events eventservice.Service, log logrus.FieldLogger) (*ServiceHandler, error) {
	if store == nil {
		return nil, errors.New("label-sync mapping store is required")
	}
	if deviceStore == nil {
		return nil, errors.New("device store is required for label-sync reconciliation")
	}
	if evaluator == nil {
		return nil, errors.New("label-sync mapping evaluator is required")
	}
	if events == nil {
		return nil, errors.New("event service is required for label-sync reconciliation")
	}
	if log == nil {
		log = logrus.New()
	}
	return &ServiceHandler{
		store:       store,
		deviceStore: deviceStore,
		evaluator:   evaluator,
		events:      events,
		log:         log,
	}, nil
}

var _ Service = (*ServiceHandler)(nil)

// SanitizeLabelSyncMapping clears status and managed metadata from an untrusted document.
func SanitizeLabelSyncMapping(mapping *domain.LabelSyncMapping) {
	if mapping == nil {
		return
	}
	mapping.Status = nil
	common.NilOutManagedObjectMetaProperties(&mapping.Metadata)
}

// CreateLabelSyncMappingFromUntrusted sanitizes an untrusted document before creation.
func CreateLabelSyncMappingFromUntrusted(ctx context.Context, svc Service, orgID uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
	SanitizeLabelSyncMapping(&mapping)
	return svc.CreateLabelSyncMapping(ctx, orgID, mapping)
}

// ReplaceLabelSyncMappingFromUntrusted sanitizes an untrusted document before replacement.
func ReplaceLabelSyncMappingFromUntrusted(ctx context.Context, svc Service, orgID uuid.UUID, name string, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
	SanitizeLabelSyncMapping(&mapping)
	return svc.ReplaceLabelSyncMapping(ctx, orgID, name, mapping)
}

func setPendingCondition(mapping *domain.LabelSyncMapping, generation int64) {
	mapping.Status = &domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{}}
	domain.SetStatusCondition(mapping.Status.Conditions, domain.Condition{
		Type:               domain.ConditionTypeLabelSyncMappingReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             "Pending",
		Message:            "Mapping propagation is pending",
		ObservedGeneration: lo.ToPtr(generation),
	})
}

func (h *ServiceHandler) CreateLabelSyncMapping(ctx context.Context, orgID uuid.UUID, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
	if errs := mapping.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if status := h.validateExpression(ctx, mapping); status.Code != domain.StatusOK().Code {
		return nil, status
	}
	setPendingCondition(&mapping, 1)
	result, err := h.store.Create(ctx, orgID, &mapping)
	return result, common.StoreErrorToApiStatus(err, true, domain.LabelSyncMappingKind, mapping.Metadata.Name)
}

func (h *ServiceHandler) ListLabelSyncMappings(ctx context.Context, orgID uuid.UUID, params domain.ListLabelSyncMappingsParams) (*domain.LabelSyncMappingList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, nil, nil, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}
	result, err := h.store.List(ctx, orgID, *listParams)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, nil)
	}
	return result, domain.StatusOK()
}

func (h *ServiceHandler) GetLabelSyncMapping(ctx context.Context, orgID uuid.UUID, name string) (*domain.LabelSyncMapping, domain.Status) {
	result, err := h.store.Get(ctx, orgID, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, &name)
	}
	return result, domain.StatusOK()
}

func (h *ServiceHandler) ReplaceLabelSyncMapping(ctx context.Context, orgID uuid.UUID, name string, mapping domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status) {
	if mapping.Metadata.Name != nil && *mapping.Metadata.Name != name {
		return nil, domain.StatusBadRequest("metadata.name does not match the URL path name")
	}
	existing, err := h.store.Get(ctx, orgID, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, &name)
	}
	if errs := existing.ValidateUpdate(&mapping); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if errs := mapping.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if status := h.validateExpression(ctx, mapping); status.Code != domain.StatusOK().Code {
		return nil, status
	}
	mapping.Metadata.Name = existing.Metadata.Name
	mapping.ApiVersion = existing.ApiVersion
	mapping.Kind = existing.Kind
	mapping.Metadata.ResourceVersion = existing.Metadata.ResourceVersion
	if reflect.DeepEqual(existing.Spec, mapping.Spec) {
		mapping.Metadata.Generation = existing.Metadata.Generation
		mapping.Status = existing.Status
	} else {
		setPendingCondition(&mapping, lo.FromPtr(existing.Metadata.Generation)+1)
	}
	result, _, err := h.store.Update(ctx, orgID, &mapping)
	return result, common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, &name)
}

func (h *ServiceHandler) validateExpression(ctx context.Context, mapping domain.LabelSyncMapping) domain.Status {
	if err := ctx.Err(); err != nil {
		return domain.StatusUnprocessableEntity(err.Error())
	}
	expectedKind := ResultKindMap
	if mapping.Spec.Key != nil {
		expectedKind = ResultKindScalar
	}
	if err := h.evaluator.ValidateExpressionIs(mapping.Spec.Expression, expectedKind); err != nil {
		return domain.StatusUnprocessableEntity(err.Error())
	}
	return domain.StatusOK()
}

func (h *ServiceHandler) DeleteLabelSyncMapping(ctx context.Context, orgID uuid.UUID, name string) domain.Status {
	deleted, err := h.store.Delete(ctx, orgID, name)
	if err != nil {
		return common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, &name)
	}
	if !deleted {
		return domain.StatusOK()
	}
	_, err = h.store.FinalizeDelete(ctx, orgID, name)
	return common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, &name)
}

func (h *ServiceHandler) PatchLabelSyncMapping(ctx context.Context, orgID uuid.UUID, name string, patch domain.PatchRequest) (*domain.LabelSyncMapping, domain.Status) {
	current, err := h.store.Get(ctx, orgID, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, &name)
	}
	updated := &domain.LabelSyncMapping{}
	if err := common.ApplyJSONPatch(ctx, current, updated, patch, "/labelsyncmappings/"+name); err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	return h.ReplaceLabelSyncMapping(ctx, orgID, name, *updated)
}
