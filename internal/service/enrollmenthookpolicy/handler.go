package enrollmenthookpolicy

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	enrollmenthookpolicystore "github.com/flightctl/flightctl/internal/store/enrollmenthookpolicy"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store  enrollmenthookpolicystore.Store
	events events.Service
	log    logrus.FieldLogger
}

func NewServiceHandler(store enrollmenthookpolicystore.Store, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, events: events, log: log}
}

var _ Service = (*ServiceHandler)(nil)

// SanitizeEnrollmentHookPolicy clears status and managed metadata from an untrusted document.
func SanitizeEnrollmentHookPolicy(policy *domain.EnrollmentHookPolicy) {
	if policy == nil {
		return
	}
	policy.Status = nil
	common.NilOutManagedObjectMetaProperties(&policy.Metadata)
}

// CreateEnrollmentHookPolicyFromUntrusted sanitizes an untrusted document, then creates it.
func CreateEnrollmentHookPolicyFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, policy domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, domain.Status) {
	SanitizeEnrollmentHookPolicy(&policy)
	return svc.CreateEnrollmentHookPolicy(ctx, orgId, policy)
}

// ReplaceEnrollmentHookPolicyFromUntrusted sanitizes an untrusted document, then replaces it.
func ReplaceEnrollmentHookPolicyFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, policy domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, domain.Status) {
	SanitizeEnrollmentHookPolicy(&policy)
	return svc.ReplaceEnrollmentHookPolicy(ctx, orgId, name, policy)
}

func (h *ServiceHandler) setReadyCondition(policy *domain.EnrollmentHookPolicy) {
	if policy.Status == nil {
		policy.Status = &domain.EnrollmentHookPolicyStatus{}
	}
	if policy.Status.Conditions == nil {
		policy.Status.Conditions = &[]domain.Condition{}
	}
	domain.SetStatusCondition(policy.Status.Conditions, domain.Condition{
		Type:   domain.ConditionType("Ready"),
		Status: domain.ConditionStatusTrue,
		Reason: "ValidationPassed",
	})
}

func (h *ServiceHandler) CreateEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, policy domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, domain.Status) {
	if errs := policy.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	h.setReadyCondition(&policy)

	result, err := h.store.Create(ctx, orgId, &policy)
	h.callbackUpdated(ctx, orgId, lo.FromPtr(policy.Metadata.Name), nil, result, true, err)
	return result, common.StoreErrorToApiStatus(err, true, domain.EnrollmentHookPolicyKind, policy.Metadata.Name)
}

func (h *ServiceHandler) ListEnrollmentHookPolicies(ctx context.Context, orgId uuid.UUID, params domain.ListEnrollmentHookPoliciesParams) (*domain.EnrollmentHookPolicyList, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}
	result, err := h.store.List(ctx, orgId, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}
	return nil, common.StoreErrorToApiStatus(err, false, domain.EnrollmentHookPolicyKind, nil)
}

func (h *ServiceHandler) GetEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentHookPolicy, domain.Status) {
	result, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.EnrollmentHookPolicyKind, &name)
	}
	return result, domain.StatusOK()
}

func (h *ServiceHandler) ReplaceEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, name string, policy domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, domain.Status) {
	if policy.Metadata.Name != nil && *policy.Metadata.Name != name {
		return nil, domain.StatusBadRequest("metadata.name does not match the URL path name")
	}

	existing, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.EnrollmentHookPolicyKind, &name)
	}

	if err := policy.PreserveSensitiveData(existing); err != nil {
		return nil, domain.StatusInternalServerError(err.Error())
	}

	if errs := policy.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	h.setReadyCondition(&policy)

	result, oldResource, err := h.store.Update(ctx, orgId, &policy)
	h.callbackUpdated(ctx, orgId, name, oldResource, result, false, err)
	return result, common.StoreErrorToApiStatus(err, false, domain.EnrollmentHookPolicyKind, &name)
}

func (h *ServiceHandler) DeleteEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	deleted, err := h.store.Delete(ctx, orgId, name)
	h.callbackDeleted(ctx, domain.EnrollmentHookPolicyKind, orgId, name, nil, nil, deleted, err)
	return common.StoreErrorToApiStatus(err, false, domain.EnrollmentHookPolicyKind, &name)
}

func (h *ServiceHandler) PatchEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.EnrollmentHookPolicy, domain.Status) {
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.EnrollmentHookPolicyKind, &name)
	}
	newObj := &domain.EnrollmentHookPolicy{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/enrollmenthookpolicies/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}
	return h.ReplaceEnrollmentHookPolicy(ctx, orgId, name, *newObj)
}

func (h *ServiceHandler) callbackUpdated(ctx context.Context, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	common.SafeEventCallback(h.log, func() {
		if err != nil {
			status := common.StoreErrorToApiStatus(err, created, domain.EnrollmentHookPolicyKind, &name)
			h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.EnrollmentHookPolicyKind, name, status, nil))
			return
		}
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.EnrollmentHookPolicyKind, name, nil, h.log, nil))
	})
}

func (h *ServiceHandler) callbackDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	common.SafeEventCallback(h.log, func() {
		h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
	})
}
