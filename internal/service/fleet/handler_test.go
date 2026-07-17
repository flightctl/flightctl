package fleet

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	statusSuccessCode     = int32(200)
	statusCreatedCode     = int32(201)
	statusBadRequestCode  = int32(400)
	statusNotFoundCode    = int32(404)
	statusConflictCode    = int32(http.StatusConflict)
	statusInternalErrCode = int32(500)
)

// fakeFleetStore is a small in-memory implementation of internal/store/fleet.Store,
// adapted from the CRUD-over-a-slice / callback-invocation behavior of
// internal/service/teststore_framework_test.go's DummyFleet (which cannot be imported
// directly since it lives in a _test.go file in a different package). Unlike DummyFleet,
// this fake implements every Store method (including UpdateStatus, UpdateConditions,
// UpdateAnnotations, OverwriteRepositoryRefs, GetRepositoryRefs, and the rollout/disruption
// list methods) so all 14 fleet.Service methods can be exercised directly.
type fakeFleetStore struct {
	fleets map[string]*domain.Fleet
	err    error

	rolloutSelectionResult *domain.FleetList
	rolloutSelectionErr    error

	disruptionBudgetResult *domain.FleetList
	disruptionBudgetErr    error

	repositoryRefs             map[string]*domain.RepositoryList
	getRepositoryRefsErr       error
	overwriteRepositoryRefsErr error
	lastOverwrittenRepoNames   []string
}

func newFakeFleetStore() *fakeFleetStore {
	return &fakeFleetStore{
		fleets:         map[string]*domain.Fleet{},
		repositoryRefs: map[string]*domain.RepositoryList{},
	}
}

func (f *fakeFleetStore) InitialMigration(ctx context.Context) error { return f.err }

func (f *fakeFleetStore) Create(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, eventCallback store.EventCallback) (*domain.Fleet, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(fleet.Metadata.Name)
	if _, exists := f.fleets[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.fleets[name] = fleet
	if eventCallback != nil {
		eventCallback(ctx, domain.FleetKind, orgId, name, nil, fleet, true, nil)
	}
	return fleet, nil
}

func (f *fakeFleetStore) Update(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, fieldsToUnset []string, fromAPI bool, eventCallback store.EventCallback) (*domain.Fleet, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(fleet.Metadata.Name)
	old, exists := f.fleets[name]
	if !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	f.fleets[name] = fleet
	if eventCallback != nil {
		eventCallback(ctx, domain.FleetKind, orgId, name, old, fleet, false, nil)
	}
	return fleet, nil
}

func (f *fakeFleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, fieldsToUnset []string, fromAPI bool, eventCallback store.EventCallback) (*domain.Fleet, bool, error) {
	name := lo.FromPtr(fleet.Metadata.Name)
	if _, exists := f.fleets[name]; exists {
		result, err := f.Update(ctx, orgId, fleet, fieldsToUnset, fromAPI, eventCallback)
		return result, false, err
	}
	result, err := f.Create(ctx, orgId, fleet, eventCallback)
	return result, true, err
}

func (f *fakeFleetStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...fleetstore.GetOption) (*domain.Fleet, error) {
	if f.err != nil {
		return nil, f.err
	}
	fleet, ok := f.fleets[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return fleet, nil
}

func (f *fakeFleetStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, opts ...fleetstore.ListOption) (*domain.FleetList, error) {
	if f.err != nil {
		return nil, f.err
	}
	items := make([]domain.Fleet, 0, len(f.fleets))
	for _, fl := range f.fleets {
		items = append(items, *fl)
	}
	return &domain.FleetList{Items: items}, nil
}

func (f *fakeFleetStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	old, exists := f.fleets[name]
	if !exists {
		return flterrors.ErrResourceNotFound
	}
	delete(f.fleets, name)
	if eventCallback != nil {
		eventCallback(ctx, domain.FleetKind, orgId, name, old, nil, false, nil)
	}
	return nil
}

func (f *fakeFleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet) (*domain.Fleet, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(fleet.Metadata.Name)
	if _, exists := f.fleets[name]; !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	f.fleets[name] = fleet
	return fleet, nil
}

func (f *fakeFleetStore) ListRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error) {
	if f.rolloutSelectionErr != nil {
		return nil, f.rolloutSelectionErr
	}
	if f.rolloutSelectionResult != nil {
		return f.rolloutSelectionResult, nil
	}
	return &domain.FleetList{}, nil
}

func (f *fakeFleetStore) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error) {
	if f.disruptionBudgetErr != nil {
		return nil, f.disruptionBudgetErr
	}
	if f.disruptionBudgetResult != nil {
		return f.disruptionBudgetResult, nil
	}
	return &domain.FleetList{}, nil
}

func (f *fakeFleetStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return f.err
}

func (f *fakeFleetStore) UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error {
	return f.err
}

func (f *fakeFleetStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, eventCallback store.EventCallback) error {
	fleet, ok := f.fleets[name]
	if !ok {
		return flterrors.ErrResourceNotFound
	}
	if fleet.Status == nil {
		fleet.Status = &domain.FleetStatus{}
	}
	old := *fleet
	for _, c := range conditions {
		domain.SetStatusCondition(&fleet.Status.Conditions, c)
	}
	if eventCallback != nil {
		eventCallback(ctx, domain.FleetKind, orgId, name, &old, fleet, false, nil)
	}
	return nil
}

func (f *fakeFleetStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string, eventCallback store.EventCallback) error {
	fleet, ok := f.fleets[name]
	if !ok {
		return flterrors.ErrResourceNotFound
	}
	old := *fleet
	existing := map[string]string{}
	if fleet.Metadata.Annotations != nil {
		for k, v := range *fleet.Metadata.Annotations {
			existing[k] = v
		}
	}
	for k, v := range annotations {
		existing[k] = v
	}
	for _, k := range deleteKeys {
		delete(existing, k)
	}
	fleet.Metadata.Annotations = &existing
	if eventCallback != nil {
		eventCallback(ctx, domain.FleetKind, orgId, name, &old, fleet, false, nil)
	}
	return nil
}

func (f *fakeFleetStore) MutateAnnotation(ctx context.Context, orgId uuid.UUID, name string, key string, mutate func(current string) (string, error)) error {
	fleet, ok := f.fleets[name]
	if !ok {
		return flterrors.ErrResourceNotFound
	}
	existing := map[string]string{}
	if fleet.Metadata.Annotations != nil {
		for k, v := range *fleet.Metadata.Annotations {
			existing[k] = v
		}
	}
	newValue, err := mutate(existing[key])
	if err != nil {
		return err
	}
	existing[key] = newValue
	fleet.Metadata.Annotations = &existing
	return nil
}

func (f *fakeFleetStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	if f.overwriteRepositoryRefsErr != nil {
		return f.overwriteRepositoryRefsErr
	}
	f.lastOverwrittenRepoNames = repositoryNames
	return nil
}

func (f *fakeFleetStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, error) {
	if f.getRepositoryRefsErr != nil {
		return nil, f.getRepositoryRefsErr
	}
	if refs, ok := f.repositoryRefs[name]; ok {
		return refs, nil
	}
	return &domain.RepositoryList{}, nil
}

func (f *fakeFleetStore) CountByRolloutStatus(ctx context.Context, orgId *uuid.UUID, _ *string) ([]fleetstore.CountByRolloutStatusResult, error) {
	return nil, f.err
}

// fakeEventsService is a recording fake for events.Service; embedding events.Service means
// only the 2 generic methods Fleet's own event logic (EmitFleetUpdatedEvent, in events.go)
// calls into need overriding. Fleet-specific decisions now live in this package, so tests
// assert on the actual events recorded via CreateEvent rather than intercepting a
// resource-specific callback.
type fakeEventsService struct {
	events.Service
	created []*domain.Event
	deleted []recordedCallback
}

type recordedCallback struct {
	orgId   uuid.UUID
	name    string
	created bool
	err     error
}

func (f *fakeEventsService) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	f.created = append(f.created, event)
}

func (f *fakeEventsService) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	f.deleted = append(f.deleted, recordedCallback{orgId: orgId, name: name, created: created, err: err})
}

func newTestHandler() (*ServiceHandler, *fakeFleetStore, *fakeEventsService) {
	fakeStore := newFakeFleetStore()
	fakeEvents := &fakeEventsService{}
	return NewServiceHandler(fakeStore, fakeEvents, logrus.New()), fakeStore, fakeEvents
}

func createTestFleet(name string, owner *string) domain.Fleet {
	return domain.Fleet{
		ApiVersion: "v1beta1",
		Kind:       "Fleet",
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
			Owner:  owner,
		},
		Spec: domain.FleetSpec{
			Selector: &domain.LabelSelector{
				MatchLabels: &map[string]string{"devKey": "devValue"},
			},
			Template: struct {
				Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
				Spec     domain.DeviceSpec  "json:\"spec\""
			}{
				Spec: domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{
						Image: "img",
					},
				},
			},
		},
	}
}

func TestCreateFleet(t *testing.T) {
	t.Run("When the fleet is valid it should create it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		fleet := createTestFleet("f1", nil)

		result, status := h.CreateFleet(context.Background(), uuid.New(), fleet)
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.fleets, "f1")
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeEvents.created[0].Reason)
	})

	t.Run("When managed metadata fields are set by the caller it should clear them before creation", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fleet := createTestFleet("f2", nil)
		fleet.Metadata.Owner = lo.ToPtr("someone")
		fleet.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.CreateFleet(context.Background(), uuid.New(), fleet)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Nil(t, fakeStore.fleets["f2"].Metadata.Owner)
		require.Nil(t, fakeStore.fleets["f2"].Metadata.Generation)
	})

	t.Run("When the store errors it should return an internal-server-error status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.err = errors.New("db down")

		_, status := h.CreateFleet(context.Background(), uuid.New(), createTestFleet("f3", nil))
		require.Equal(t, statusInternalErrCode, status.Code)
	})
}

func TestListFleets(t *testing.T) {
	t.Run("When the store succeeds it should return the list with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fl := createTestFleet("f1", nil)
		fakeStore.fleets["f1"] = &fl

		result, status := h.ListFleets(context.Background(), uuid.New(), domain.ListFleetsParams{})
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})

	t.Run("When the field selector is invalid it should return a bad-request status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		badSelector := "%%%invalid%%%"

		_, status := h.ListFleets(context.Background(), uuid.New(), domain.ListFleetsParams{FieldSelector: &badSelector})
		require.Equal(t, statusBadRequestCode, status.Code)
	})
}

func TestGetFleet(t *testing.T) {
	t.Run("When the fleet exists it should return it with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fl := createTestFleet("f1", nil)
		fakeStore.fleets["f1"] = &fl

		result, status := h.GetFleet(context.Background(), uuid.New(), "f1", domain.GetFleetParams{})
		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, "f1", lo.FromPtr(result.Metadata.Name))
	})

	t.Run("When the fleet does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()

		_, status := h.GetFleet(context.Background(), uuid.New(), "missing", domain.GetFleetParams{})
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}

func TestReplaceFleet(t *testing.T) {
	t.Run("When the fleet does not exist it should create it", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fleet := createTestFleet("new-fleet", nil)

		result, status := h.ReplaceFleet(context.Background(), uuid.New(), "new-fleet", fleet)
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.fleets, "new-fleet")
	})

	t.Run("When the name in the path does not match metadata.name it should return a bad-request status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		fleet := createTestFleet("f1", nil)

		_, status := h.ReplaceFleet(context.Background(), uuid.New(), "different-name", fleet)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When the fleet exists it should update it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		orgId := uuid.New()
		fleet := createTestFleet("f1", nil)
		_, status := h.CreateFleet(context.Background(), orgId, fleet)
		require.Equal(t, statusCreatedCode, status.Code)

		result, status := h.ReplaceFleet(context.Background(), orgId, "f1", fleet)
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.fleets, "f1")
		// Only the create produces a ResourceCreated event; replacing with identical
		// metadata (no generation/labels/owner change) emits nothing further.
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeEvents.created[0].Reason)
	})
}

func TestReplaceFleetOwnership(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	t.Run("When replacing an owned fleet with a changed spec it should return conflict", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		existing := createTestFleet("owned-fleet", &owner)
		fakeStore.fleets["owned-fleet"] = &existing

		updated := createTestFleet("owned-fleet", nil)
		updated.Spec.Template.Spec.Os.Image = "img-updated"

		_, status := h.ReplaceFleet(context.Background(), orgId, "owned-fleet", updated)
		require.Equal(t, statusConflictCode, status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
		require.Equal(t, "img", fakeStore.fleets["owned-fleet"].Spec.Template.Spec.Os.Image)
	})

	t.Run("When replacing an owned fleet with changed labels it should return conflict", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		existing := createTestFleet("owned-fleet", &owner)
		fakeStore.fleets["owned-fleet"] = &existing

		updated := createTestFleet("owned-fleet", nil)
		updated.Metadata.Labels = &map[string]string{"labelKey": "changed"}

		_, status := h.ReplaceFleet(context.Background(), orgId, "owned-fleet", updated)
		require.Equal(t, statusConflictCode, status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
	})

	t.Run("When ResourceSync replaces an owned fleet it should allow the update", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		existing := createTestFleet("owned-fleet", &owner)
		fakeStore.fleets["owned-fleet"] = &existing

		updated := createTestFleet("owned-fleet", nil)
		updated.Spec.Template.Spec.Os.Image = "img-updated"
		ctx := context.WithValue(context.Background(), consts.ResourceSyncRequestCtxKey, true)

		result, status := h.ReplaceFleet(ctx, orgId, "owned-fleet", updated)
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Equal(t, "img-updated", fakeStore.fleets["owned-fleet"].Spec.Template.Spec.Os.Image)
	})

	t.Run("When an internal request replaces an owned fleet it should allow the update", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		existing := createTestFleet("owned-fleet", &owner)
		fakeStore.fleets["owned-fleet"] = &existing

		updated := createTestFleet("owned-fleet", nil)
		updated.Spec.Template.Spec.Os.Image = "img-updated"
		ctx := context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)

		result, status := h.ReplaceFleet(ctx, orgId, "owned-fleet", updated)
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Equal(t, "img-updated", fakeStore.fleets["owned-fleet"].Spec.Template.Spec.Os.Image)
	})

	t.Run("When replacing an unowned fleet with a changed spec it should allow the update", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		orgId := uuid.New()
		existing := createTestFleet("unowned-fleet", nil)
		fakeStore.fleets["unowned-fleet"] = &existing

		updated := createTestFleet("unowned-fleet", nil)
		updated.Spec.Template.Spec.Os.Image = "img-updated"

		result, status := h.ReplaceFleet(context.Background(), orgId, "unowned-fleet", updated)
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Equal(t, "img-updated", fakeStore.fleets["unowned-fleet"].Spec.Template.Spec.Os.Image)
	})
}

func TestPatchFleetOwnership(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	t.Run("When patching an owned fleet spec it should return conflict", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		existing := createTestFleet("owned-fleet", &owner)
		fakeStore.fleets["owned-fleet"] = &existing

		var valueIface interface{} = "img-updated"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/template/spec/os/image", Value: &valueIface}}

		_, status := h.PatchFleet(context.Background(), uuid.New(), "owned-fleet", patch)
		require.Equal(t, statusConflictCode, status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
		require.Equal(t, "img", fakeStore.fleets["owned-fleet"].Spec.Template.Spec.Os.Image)
	})

	t.Run("When patching an owned fleet labels it should return conflict", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		existing := createTestFleet("owned-fleet", &owner)
		fakeStore.fleets["owned-fleet"] = &existing

		var valueIface interface{} = "changed"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels/labelKey", Value: &valueIface}}

		_, status := h.PatchFleet(context.Background(), uuid.New(), "owned-fleet", patch)
		require.Equal(t, statusConflictCode, status.Code)
		require.Equal(t, flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error(), status.Message)
	})

	t.Run("When ResourceSync patches an owned fleet it should allow the update", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		existing := createTestFleet("owned-fleet", &owner)
		fakeStore.fleets["owned-fleet"] = &existing

		var valueIface interface{} = "img-updated"
		patch := domain.PatchRequest{{Op: "replace", Path: "/spec/template/spec/os/image", Value: &valueIface}}
		ctx := context.WithValue(context.Background(), consts.ResourceSyncRequestCtxKey, true)

		result, status := h.PatchFleet(ctx, uuid.New(), "owned-fleet", patch)
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Equal(t, "img-updated", fakeStore.fleets["owned-fleet"].Spec.Template.Spec.Os.Image)
	})
}

func TestDeleteFleet(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	tests := []struct {
		name                  string
		fleetName             string
		fleetOwner            *string
		createFleet           bool
		isResourceSyncRequest bool
		expectedStatusCode    int32
		expectedError         error
		expectFleetDeleted    bool
	}{
		{
			name:               "delete fleet without owner succeeds",
			fleetName:          "test-fleet",
			fleetOwner:         nil,
			createFleet:        true,
			expectedStatusCode: statusSuccessCode,
			expectFleetDeleted: true,
		},
		{
			name:               "delete non-existent fleet returns OK (idempotent)",
			fleetName:          "nonexistent-fleet",
			createFleet:        false,
			expectedStatusCode: statusSuccessCode,
			expectFleetDeleted: true,
		},
		{
			name:               "delete fleet with owner fails with conflict",
			fleetName:          "owned-fleet",
			fleetOwner:         &owner,
			createFleet:        true,
			expectedStatusCode: statusConflictCode,
			expectedError:      flterrors.ErrDeletingResourceWithOwnerNotAllowed,
			expectFleetDeleted: false,
		},
		{
			name:                  "resourceSync can delete fleets it owns",
			fleetName:             "resourcesync-owned-fleet",
			fleetOwner:            &owner,
			createFleet:           true,
			isResourceSyncRequest: true,
			expectedStatusCode:    statusSuccessCode,
			expectFleetDeleted:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fakeStore, fakeEvents := newTestHandler()
			ctx := context.Background()
			testOrgId := uuid.New()

			if tt.createFleet {
				fleet := createTestFleet(tt.fleetName, tt.fleetOwner)
				fakeStore.fleets[tt.fleetName] = &fleet
			}

			deleteCtx := ctx
			if tt.isResourceSyncRequest {
				deleteCtx = context.WithValue(ctx, consts.ResourceSyncRequestCtxKey, true)
			}

			status := h.DeleteFleet(deleteCtx, testOrgId, tt.fleetName)
			require.Equal(t, tt.expectedStatusCode, status.Code)

			if tt.expectedError != nil {
				require.Equal(t, tt.expectedError.Error(), status.Message)
			}

			_, ok := fakeStore.fleets[tt.fleetName]
			require.Equal(t, !tt.expectFleetDeleted, ok)

			// Verify the deletion callback wiring survived extraction: a successful delete
			// of a pre-existing fleet must invoke events.HandleGenericResourceDeletedEvents.
			if tt.createFleet && tt.expectFleetDeleted {
				require.Len(t, fakeEvents.deleted, 1)
				require.Equal(t, tt.fleetName, fakeEvents.deleted[0].name)
			} else {
				require.Empty(t, fakeEvents.deleted)
			}
		})
	}
}

func TestGetFleetStatus(t *testing.T) {
	h, fakeStore, _ := newTestHandler()
	fl := createTestFleet("f1", nil)
	fakeStore.fleets["f1"] = &fl

	result, status := h.GetFleetStatus(context.Background(), uuid.New(), "f1")
	require.Equal(t, domain.StatusOK(), status)
	require.Equal(t, "f1", lo.FromPtr(result.Metadata.Name))

	_, status = h.GetFleetStatus(context.Background(), uuid.New(), "missing")
	require.Equal(t, statusNotFoundCode, status.Code)
}

func TestReplaceFleetStatus(t *testing.T) {
	t.Run("When the fleet exists it should update its status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fl := createTestFleet("f1", nil)
		fakeStore.fleets["f1"] = &fl

		result, status := h.ReplaceFleetStatus(context.Background(), uuid.New(), "f1", fl)
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
	})

	t.Run("When the fleet does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		fl := createTestFleet("missing", nil)

		_, status := h.ReplaceFleetStatus(context.Background(), uuid.New(), "missing", fl)
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}

func testFleetPatch(t *testing.T, patch domain.PatchRequest) (*domain.Fleet, domain.Fleet, domain.Status) {
	require := require.New(t)
	orig := createTestFleet("foo", nil)
	orig.Status = &domain.FleetStatus{
		Conditions: []domain.Condition{
			{Type: "Approved", Status: "True"},
		},
	}

	h, fakeStore, _ := newTestHandler()
	fakeStore.fleets["foo"] = &orig

	resp, status := h.PatchFleet(context.Background(), uuid.New(), "foo", patch)
	require.NotEqual(int32(0), status.Code)
	return resp, orig, status
}

func verifyFleetPatchFailed(t *testing.T, status domain.Status) {
	require.Equal(t, statusBadRequestCode, status.Code)
}

func TestFleetPatchName(t *testing.T) {
	var value interface{} = "bar"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	_, _, status := testFleetPatch(t, pr)
	verifyFleetPatchFailed(t, status)

	pr = domain.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	_, _, status = testFleetPatch(t, pr)
	verifyFleetPatchFailed(t, status)
}

func TestFleetPatchKind(t *testing.T) {
	var value interface{} = "bar"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	_, _, status := testFleetPatch(t, pr)
	verifyFleetPatchFailed(t, status)
}

func TestFleetPatchAPIVersion(t *testing.T) {
	var value interface{} = "bar"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	_, _, status := testFleetPatch(t, pr)
	verifyFleetPatchFailed(t, status)
}

func TestFleetPatchSpec(t *testing.T) {
	value := "newimg"
	var valueIface interface{} = value
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/spec/template/spec/os/image", Value: &valueIface},
	}
	resp, orig, status := testFleetPatch(t, pr)
	orig.Spec.Template.Spec.Os.Image = value
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, orig, *resp)
}

func TestFleetPatchNonExistingPath(t *testing.T) {
	var value interface{} = "foo"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/spec/doesnotexist", Value: &value},
	}
	_, _, status := testFleetPatch(t, pr)
	verifyFleetPatchFailed(t, status)
}

func TestFleetPatchLabels(t *testing.T) {
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, orig, status := testFleetPatch(t, pr)
	orig.Metadata.Labels = &addLabels
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, orig, *resp)
}

func TestPatchFleetNonExistingResource(t *testing.T) {
	h, _, _ := newTestHandler()
	var value interface{} = "labelValue1"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, status := h.PatchFleet(context.Background(), uuid.New(), "doesnotexist", pr)
	require.Equal(t, statusNotFoundCode, status.Code)
	require.Nil(t, resp)
}

func TestListFleetRolloutDeviceSelection(t *testing.T) {
	t.Run("When the store succeeds it should return the list", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fl := createTestFleet("f1", nil)
		fakeStore.rolloutSelectionResult = &domain.FleetList{Items: []domain.Fleet{fl}}

		result, status := h.ListFleetRolloutDeviceSelection(context.Background(), uuid.New())
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})

	t.Run("When the store errors it should return an internal-server-error status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.rolloutSelectionErr = errors.New("db down")

		_, status := h.ListFleetRolloutDeviceSelection(context.Background(), uuid.New())
		require.Equal(t, statusInternalErrCode, status.Code)
	})
}

func TestListDisruptionBudgetFleets(t *testing.T) {
	t.Run("When the store succeeds it should return the list", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fl := createTestFleet("f1", nil)
		fakeStore.disruptionBudgetResult = &domain.FleetList{Items: []domain.Fleet{fl}}

		result, status := h.ListDisruptionBudgetFleets(context.Background(), uuid.New())
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})

	t.Run("When the store errors it should return an internal-server-error status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.disruptionBudgetErr = errors.New("db down")

		_, status := h.ListDisruptionBudgetFleets(context.Background(), uuid.New())
		require.Equal(t, statusInternalErrCode, status.Code)
	})
}

func TestUpdateFleetConditions(t *testing.T) {
	t.Run("When the fleet exists it should update its conditions without emitting an unrelated event", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		fl := createTestFleet("f1", nil)
		fakeStore.fleets["f1"] = &fl

		status := h.UpdateFleetConditions(context.Background(), uuid.New(), "f1", []domain.Condition{
			{Type: "Approved", Status: "True"},
		})
		require.Equal(t, statusSuccessCode, status.Code)
		// A non-FleetValid condition change doesn't touch ObjectMeta and isn't the
		// FleetValid condition emitFleetValidEvents watches, so no event is emitted.
		require.Empty(t, fakeEvents.created)
	})

	t.Run("When the fleet does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()

		status := h.UpdateFleetConditions(context.Background(), uuid.New(), "missing", []domain.Condition{})
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}

func TestUpdateFleetAnnotations(t *testing.T) {
	t.Run("When the fleet exists it should update its annotations without emitting an unrelated event", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		fl := createTestFleet("f1", nil)
		fakeStore.fleets["f1"] = &fl

		status := h.UpdateFleetAnnotations(context.Background(), uuid.New(), "f1", map[string]string{"k": "v"}, nil)
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "v", (*fakeStore.fleets["f1"].Metadata.Annotations)["k"])
		// Annotations aren't tracked by ComputeResourceUpdatedDetails, so no event is emitted.
		require.Empty(t, fakeEvents.created)
	})

	t.Run("When the fleet does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()

		status := h.UpdateFleetAnnotations(context.Background(), uuid.New(), "missing", map[string]string{"k": "v"}, nil)
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}

func TestOverwriteFleetRepositoryRefs(t *testing.T) {
	t.Run("When the store succeeds it should overwrite the refs", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()

		status := h.OverwriteFleetRepositoryRefs(context.Background(), uuid.New(), "f1", "repo-a", "repo-b")
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, []string{"repo-a", "repo-b"}, fakeStore.lastOverwrittenRepoNames)
	})

	t.Run("When the store errors it should return an internal-server-error status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.overwriteRepositoryRefsErr = errors.New("db down")

		status := h.OverwriteFleetRepositoryRefs(context.Background(), uuid.New(), "f1", "repo-a")
		require.Equal(t, statusInternalErrCode, status.Code)
	})
}

func TestGetFleetRepositoryRefs(t *testing.T) {
	t.Run("When the store succeeds it should return the refs", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.repositoryRefs["f1"] = &domain.RepositoryList{Items: []domain.Repository{
			{Metadata: domain.ObjectMeta{Name: lo.ToPtr("repo-a")}},
		}}

		result, status := h.GetFleetRepositoryRefs(context.Background(), uuid.New(), "f1")
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})

	t.Run("When the store errors it should return an internal-server-error status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.getRepositoryRefsErr = errors.New("db down")

		_, status := h.GetFleetRepositoryRefs(context.Background(), uuid.New(), "f1")
		require.Equal(t, statusInternalErrCode, status.Code)
	})
}
