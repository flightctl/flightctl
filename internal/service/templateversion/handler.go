package templateversion

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store   templateversionstore.Store
	kvStore kvstore.KVStore
	events  events.Service
	log     logrus.FieldLogger
}

// NewServiceHandler creates a new templateversion ServiceHandler instance.
func NewServiceHandler(store templateversionstore.Store, kvStore kvstore.KVStore, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, kvStore: kvStore, events: events, log: log}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateTemplateVersion(ctx context.Context, orgId uuid.UUID, templateVersion domain.TemplateVersion, immediateRollout bool) (*domain.TemplateVersion, domain.Status) {
	if errs := templateVersion.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := h.store.Create(ctx, orgId, &templateVersion)
	h.callbackTemplateVersionUpdated(ctx, domain.TemplateVersionKind, orgId, lo.FromPtr(templateVersion.Metadata.Name), nil, result, true, err)
	if err == nil {
		fleet.EmitFleetRolloutStartedEvent(ctx, h.events, orgId, lo.FromPtr(templateVersion.Metadata.Name), templateVersion.Spec.Fleet, immediateRollout)
	}
	return result, common.StoreErrorToApiStatus(err, true, domain.TemplateVersionKind, templateVersion.Metadata.Name)
}

func (h *ServiceHandler) ListTemplateVersions(ctx context.Context, orgId uuid.UUID, fleet string, params domain.ListTemplateVersionsParams) (*domain.TemplateVersionList, domain.Status) {
	var err error

	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	// sort primarily by created_at with desc (newest first)
	listParams.SortColumns = []store.SortColumn{store.SortByCreatedAt, store.SortByName}
	listParams.SortOrder = lo.ToPtr(store.SortDesc)

	var fieldSelector *selector.FieldSelector
	if fieldSelector, err = selector.NewFieldSelectorFromMap(map[string]string{"metadata.owner": fleet}); err != nil {
		return nil, domain.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))
	}

	// If additional field selectors are provided, merge them
	if params.FieldSelector != nil {
		additionalSelector, err := selector.NewFieldSelector(*params.FieldSelector)
		if err != nil {
			return nil, domain.StatusBadRequest(fmt.Sprintf("failed to parse additional field selector: %v", err))
		}
		fieldSelector.Add(additionalSelector)
	}

	listParams.FieldSelector = fieldSelector
	result, err := h.store.List(ctx, orgId, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*domain.TemplateVersion, domain.Status) {
	result, err := h.store.Get(ctx, orgId, fleet, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.TemplateVersionKind, &name)
}

func (h *ServiceHandler) DeleteTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) domain.Status {
	tvkey := kvstore.TemplateVersionKey{OrgID: orgId, Fleet: fleet, TemplateVersion: name}
	err := h.kvStore.DeleteKeysForTemplateVersion(ctx, tvkey.ComposeKey())
	if err != nil {
		h.log.Warnf("failed deleting KV storage for templateVersion %s/%s/%s", orgId, fleet, name)
	}

	deleted, err := h.store.Delete(ctx, orgId, fleet, name)
	if err == nil && deleted {
		h.callbackTemplateVersionDeleted(ctx, domain.TemplateVersionKind, orgId, name, nil, nil, false, nil)
	}
	return common.StoreErrorToApiStatus(err, false, domain.TemplateVersionKind, &name)
}

func (h *ServiceHandler) GetLatestTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string) (*domain.TemplateVersion, domain.Status) {
	result, err := h.store.GetLatest(ctx, orgId, fleet)
	return result, common.StoreErrorToApiStatus(err, false, domain.TemplateVersionKind, nil)
}

// callbackTemplateVersionUpdated is the template version-specific callback that handles template version events
func (h *ServiceHandler) callbackTemplateVersionUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	store.SafeEventCallback(h.log, func() {
		if err != nil {
			status := common.StoreErrorToApiStatus(err, created, string(resourceKind), &name)
			h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, resourceKind, name, status, nil))
		} else {
			// Compute ResourceUpdatedDetails for updates
			var updateDetails *domain.ResourceUpdatedDetails
			if !created {
				var (
					oldTemplateVersion, newTemplateVersion *domain.TemplateVersion
					ok                                     bool
				)
				if oldTemplateVersion, newTemplateVersion, ok = common.CastResources[domain.TemplateVersion](oldResource, newResource); ok && oldTemplateVersion != nil && newTemplateVersion != nil {
					updateDetails = common.ComputeResourceUpdatedDetails(oldTemplateVersion.Metadata, newTemplateVersion.Metadata)
				}
			}
			h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, resourceKind, name, updateDetails, h.log, nil))
		}
	})
}

// callbackTemplateVersionDeleted is the template version-specific callback that handles template version deletion events
func (h *ServiceHandler) callbackTemplateVersionDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	store.SafeEventCallback(h.log, func() {
		h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
	})
}
