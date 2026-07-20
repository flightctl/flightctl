package organization

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	"github.com/google/uuid"
)

var organizationApiVersion = fmt.Sprintf("%s/%s", domain.APIGroup, domain.OrganizationAPIVersion)

type ServiceHandler struct {
	store organizationstore.Store
}

// NewServiceHandler creates a new organization ServiceHandler instance.
func NewServiceHandler(store organizationstore.Store) *ServiceHandler {
	return &ServiceHandler{store: store}
}

var _ Service = (*ServiceHandler)(nil)

// organizationModelToAPI converts a model.Organization to domain.Organization
func organizationModelToAPI(org *model.Organization) domain.Organization {
	name := org.ID.String()
	return domain.Organization{
		ApiVersion: organizationApiVersion,
		Kind:       domain.OrganizationKind,
		Metadata:   domain.ObjectMeta{Name: &name},
		Spec: &domain.OrganizationSpec{
			ExternalId:  &org.ExternalID,
			DisplayName: &org.DisplayName,
		},
	}
}

// ListOrganizations returns the organizations the caller's mapped identity belongs to.
func (h *ServiceHandler) ListOrganizations(ctx context.Context, params domain.ListOrganizationsParams) (*domain.OrganizationList, domain.Status) {
	userOrgs, err := h.listUserOrganizations(ctx)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.OrganizationKind, nil)
	}

	if params.FieldSelector == nil || *params.FieldSelector == "" {
		return buildOrganizationList(userOrgs), domain.StatusOK()
	}

	allowedIDs := parseFieldSelectorForOrgIDs(*params.FieldSelector)
	filtered := make([]*model.Organization, 0, len(userOrgs))
	for _, userOrg := range userOrgs {
		if _, exists := allowedIDs[userOrg.ID]; exists {
			filtered = append(filtered, userOrg)
		}
	}
	return buildOrganizationList(filtered), domain.StatusOK()
}

// ListAllOrganizations returns every organization in the system, bypassing identity-based scoping.
// It is intended for trusted internal callers (workers, periodic tasks, alert exporter) that need
// a system-wide view rather than the requesting identity's own organizations.
func (h *ServiceHandler) ListAllOrganizations(ctx context.Context, params domain.ListOrganizationsParams) (*domain.OrganizationList, domain.Status) {
	listParams, status := common.PrepareListParams(nil, nil, params.FieldSelector, nil)
	if status.Code != http.StatusOK {
		return nil, status
	}

	orgs, err := h.store.List(ctx, *listParams)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.OrganizationKind, nil)
	}

	return buildOrganizationList(orgs), domain.StatusOK()
}

func buildOrganizationList(orgs []*model.Organization) *domain.OrganizationList {
	apiOrgs := make([]domain.Organization, len(orgs))
	for i, org := range orgs {
		apiOrgs[i] = organizationModelToAPI(org)
	}

	return &domain.OrganizationList{
		Items:      apiOrgs,
		ApiVersion: organizationApiVersion,
		Kind:       domain.OrganizationListKind,
		Metadata:   domain.ListMeta{},
	}
}

func (h *ServiceHandler) listUserOrganizations(ctx context.Context) ([]*model.Organization, error) {
	// Get mapped identity from context (set by identity mapping middleware)
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no mapped identity found in context")
	}

	return mappedIdentity.Organizations, nil
}

func parseFieldSelectorForOrgIDs(selectorStr string) map[uuid.UUID]struct{} {
	selectorRegex := regexp.MustCompile(`["']?([0-9a-fA-F-]{36})["']?`)

	allowedIDs := make(map[uuid.UUID]struct{})
	matches := selectorRegex.FindAllStringSubmatch(selectorStr, -1)

	for _, match := range matches {
		if id, err := uuid.Parse(match[1]); err == nil {
			allowedIDs[id] = struct{}{}
		}
	}
	return allowedIDs
}
