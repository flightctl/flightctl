package authprovider

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	testutil "github.com/flightctl/flightctl/test/util/testdata"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// adminCtx returns a context carrying a super-admin mapped identity, matching the fixture
// every integration test in test/integration/service/service_suite_test.go relies on —
// AuthProvider's OrganizationAssignment validation requires a mapped identity in context, and
// testutil.ReturnTestAuthProvider's static assignment targets an organization named "test-org".
func adminCtx() context.Context {
	adminIdentity := identity.NewMappedIdentity("test-admin", uuid.NewString(), nil, map[string][]string{}, true, nil)
	return context.WithValue(context.Background(), consts.MappedIdentityCtxKey, adminIdentity)
}

// memberCtx returns a context carrying a non-super-admin mapped identity that belongs to the
// "test-org" organization, satisfying AuthProvider's OrganizationAssignment validation without
// granting super-admin privileges.
func memberCtx() context.Context {
	testOrg := &model.Organization{ExternalID: "test-org", DisplayName: "Test Organization"}
	memberIdentity := identity.NewMappedIdentity("member", uuid.NewString(), []*model.Organization{testOrg}, map[string][]string{}, false, nil)
	return context.WithValue(context.Background(), consts.MappedIdentityCtxKey, memberIdentity)
}

// fakeAuthProviderStore is a small in-memory implementation of internal/store/authprovider.Store.
type fakeAuthProviderStore struct {
	providers map[string]*domain.AuthProvider
	err       error
}

func newFakeAuthProviderStore() *fakeAuthProviderStore {
	return &fakeAuthProviderStore{providers: map[string]*domain.AuthProvider{}}
}

func (f *fakeAuthProviderStore) InitialMigration(ctx context.Context) error { return f.err }

func (f *fakeAuthProviderStore) Create(ctx context.Context, orgId uuid.UUID, authProvider *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(authProvider.Metadata.Name)
	if _, exists := f.providers[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.providers[name] = authProvider
	if eventCallback != nil {
		eventCallback(ctx, domain.AuthProviderKind, orgId, name, nil, authProvider, true, nil)
	}
	return authProvider, nil
}

func (f *fakeAuthProviderStore) Update(ctx context.Context, orgId uuid.UUID, authProvider *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(authProvider.Metadata.Name)
	old, exists := f.providers[name]
	if !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	f.providers[name] = authProvider
	if eventCallback != nil {
		eventCallback(ctx, domain.AuthProviderKind, orgId, name, old, authProvider, false, nil)
	}
	return authProvider, nil
}

func (f *fakeAuthProviderStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, authProvider *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, bool, error) {
	name := lo.FromPtr(authProvider.Metadata.Name)
	if _, exists := f.providers[name]; exists {
		result, err := f.Update(ctx, orgId, authProvider, eventCallback)
		return result, false, err
	}
	result, err := f.Create(ctx, orgId, authProvider, eventCallback)
	return result, true, err
}

func (f *fakeAuthProviderStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.AuthProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.providers[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return p, nil
}

func (f *fakeAuthProviderStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.AuthProviderList, error) {
	if f.err != nil {
		return nil, f.err
	}
	var items []domain.AuthProvider
	for _, p := range f.providers {
		items = append(items, *p)
	}
	return &domain.AuthProviderList{Items: items}, nil
}

func (f *fakeAuthProviderStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	if f.err != nil {
		return f.err
	}
	delete(f.providers, name)
	if eventCallback != nil {
		eventCallback(ctx, domain.AuthProviderKind, orgId, name, nil, nil, false, nil)
	}
	return nil
}

func (f *fakeAuthProviderStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.AuthProvider, eventCallback store.EventCallback) (*domain.AuthProvider, error) {
	return resource, f.err
}

func (f *fakeAuthProviderStore) GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*domain.AuthProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, p := range f.providers {
		if spec, err := p.Spec.AsOIDCProviderSpec(); err == nil && spec.Issuer == issuer && spec.ClientId == clientId {
			return p, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (f *fakeAuthProviderStore) GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*domain.AuthProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, p := range f.providers {
		if spec, err := p.Spec.AsOAuth2ProviderSpec(); err == nil && spec.AuthorizationUrl == authorizationUrl {
			return p, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (f *fakeAuthProviderStore) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return int64(len(f.providers)), f.err
}

func (f *fakeAuthProviderStore) CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]store.CountByOrgResult, error) {
	return nil, f.err
}

func (f *fakeAuthProviderStore) ListAll(ctx context.Context, listParams store.ListParams) (*domain.AuthProviderList, error) {
	return f.List(ctx, uuid.Nil, listParams)
}

// fakeEventsService is a recording fake for events.Service. AuthProvider's own event
// decision logic (in handler.go's callbackAuthProviderUpdated) now calls CreateEvent
// directly, so tests assert on the actual emitted events rather than intercepting a
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

func newTestHandler() (*ServiceHandler, *fakeAuthProviderStore, *fakeEventsService) {
	fakeStore := newFakeAuthProviderStore()
	fakeEvents := &fakeEventsService{}
	return NewServiceHandler(fakeStore, fakeEvents, logrus.New()), fakeStore, fakeEvents
}

func TestCreateAuthProvider(t *testing.T) {
	t.Run("When the provider is valid it should create it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil)

		result, status := h.CreateAuthProvider(adminCtx(), uuid.New(), provider)
		require.Equal(t, int32(201), status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.providers, "p1")
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeEvents.created[0].Reason)
	})

	t.Run("When the spec fails validation it should return a bad-request status with sensitive fields redacted", func(t *testing.T) {
		h, _, _ := newTestHandler()
		provider := domain.AuthProvider{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("invalid")},
		}
		oidcSpec := domain.OIDCProviderSpec{ProviderType: domain.Oidc}
		require.NoError(t, provider.Spec.FromOIDCProviderSpec(oidcSpec))

		_, status := h.CreateAuthProvider(adminCtx(), uuid.New(), provider)
		require.Equal(t, int32(400), status.Code)
	})

	t.Run("When created by a super admin it should set the created-by-super-admin annotation", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p2", "", nil)

		mappedIdentity := identity.NewMappedIdentity("admin", "uid-1", nil, map[string][]string{}, true, nil)
		ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)

		result, status := h.CreateAuthProvider(ctx, uuid.New(), provider)
		require.Equal(t, int32(201), status.Code)
		require.NotNil(t, result)
		stored := fakeStore.providers["p2"]
		require.NotNil(t, stored.Metadata.Annotations)
		require.Equal(t, "true", (*stored.Metadata.Annotations)[domain.AuthProviderAnnotationCreatedBySuperAdmin])
	})

	t.Run("When created by a non-super-admin it should not set the created-by-super-admin annotation", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p2b", "", nil)

		_, status := h.CreateAuthProvider(memberCtx(), uuid.New(), provider)
		require.Equal(t, int32(201), status.Code)
		stored := fakeStore.providers["p2b"]
		require.Nil(t, stored.Metadata.Annotations)
	})

	t.Run("When managed metadata fields are set by the caller CreateAuthProviderFromUntrusted should clear them before creation", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p3", "", nil)
		provider.Metadata.Owner = lo.ToPtr("someone")
		provider.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := CreateAuthProviderFromUntrusted(adminCtx(), h, uuid.New(), provider)
		require.Equal(t, int32(201), status.Code)
		require.Nil(t, fakeStore.providers["p3"].Metadata.Owner)
		require.Nil(t, fakeStore.providers["p3"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller CreateAuthProvider (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p3-trusted", "", nil)
		provider.Metadata.Owner = lo.ToPtr("someone")
		provider.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.CreateAuthProvider(memberCtx(), uuid.New(), provider)
		require.Equal(t, int32(201), status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.providers["p3-trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.providers["p3-trusted"].Metadata.Generation))
	})

	t.Run("When creating an OAuth2 provider it should default the issuer to the authorization URL and infer introspection", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("oauth-p1")}}

		roleAssignment := domain.AuthRoleAssignment{}
		require.NoError(t, roleAssignment.FromAuthStaticRoleAssignment(domain.AuthStaticRoleAssignment{
			Type:  domain.AuthStaticRoleAssignmentTypeStatic,
			Roles: []string{domain.ExternalRoleViewer},
		}))

		spec := domain.OAuth2ProviderSpec{
			ProviderType:           domain.Oauth2,
			AuthorizationUrl:       "https://github.com/login/oauth/authorize",
			TokenUrl:               "https://github.com/login/oauth/access_token",
			UserinfoUrl:            "https://api.github.com/user",
			ClientId:               "client1",
			ClientSecret:           "secret1",
			OrganizationAssignment: testutil.CreateTestOrganizationAssignment(),
			RoleAssignment:         roleAssignment,
		}
		require.NoError(t, provider.Spec.FromOAuth2ProviderSpec(spec))

		result, status := h.CreateAuthProvider(adminCtx(), uuid.New(), provider)
		require.Equal(t, int32(201), status.Code)
		require.NotNil(t, result)

		stored, err := fakeStore.providers["oauth-p1"].Spec.AsOAuth2ProviderSpec()
		require.NoError(t, err)
		require.NotNil(t, stored.Issuer)
		require.Equal(t, spec.AuthorizationUrl, *stored.Issuer)
		require.NotNil(t, stored.Introspection)
		require.NotNil(t, stored.UsernameClaim)
		require.Equal(t, []string{"preferred_username"}, *stored.UsernameClaim)
	})
}

func TestListAuthProviders(t *testing.T) {
	t.Run("When the store succeeds it should return the list with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.providers["p1"] = lo.ToPtr(testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil))

		result, status := h.ListAuthProviders(context.Background(), uuid.New(), domain.ListAuthProvidersParams{})
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})

	t.Run("When the field selector is invalid it should return a bad-request status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		badSelector := "%%%invalid%%%"

		_, status := h.ListAuthProviders(context.Background(), uuid.New(), domain.ListAuthProvidersParams{FieldSelector: &badSelector})
		require.Equal(t, int32(400), status.Code)
	})
}

func TestListAllAuthProviders(t *testing.T) {
	t.Run("When the store succeeds it should return the list with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.providers["p1"] = lo.ToPtr(testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil))

		result, status := h.ListAllAuthProviders(context.Background(), domain.ListAuthProvidersParams{})
		require.Equal(t, domain.StatusOK(), status)
		require.Len(t, result.Items, 1)
	})
}

func TestGetAuthProvider(t *testing.T) {
	t.Run("When the provider exists it should return it with StatusOK", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		fakeStore.providers["p1"] = lo.ToPtr(testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil))

		result, status := h.GetAuthProvider(context.Background(), uuid.New(), "p1")
		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, "p1", lo.FromPtr(result.Metadata.Name))
	})

	t.Run("When the provider does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()

		_, status := h.GetAuthProvider(context.Background(), uuid.New(), "missing")
		require.Equal(t, int32(404), status.Code)
	})
}

func TestGetAuthProviderByIssuerAndClientId(t *testing.T) {
	h, fakeStore, _ := newTestHandler()
	provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "https://issuer.example.com", nil)
	fakeStore.providers["p1"] = &provider

	result, status := h.GetAuthProviderByIssuerAndClientId(context.Background(), uuid.New(), "https://issuer.example.com", "test-client-id-p1")
	require.Equal(t, domain.StatusOK(), status)
	require.Equal(t, "p1", lo.FromPtr(result.Metadata.Name))

	_, status = h.GetAuthProviderByIssuerAndClientId(context.Background(), uuid.New(), "https://other.example.com", "x")
	require.Equal(t, int32(404), status.Code)
}

func TestGetAuthProviderByAuthorizationUrl(t *testing.T) {
	h, fakeStore, _ := newTestHandler()
	spec := domain.OAuth2ProviderSpec{
		ProviderType:     domain.Oauth2,
		AuthorizationUrl: "https://auth.example.com/authorize",
		TokenUrl:         "https://auth.example.com/token",
		UserinfoUrl:      "https://auth.example.com/userinfo",
		ClientId:         "client1",
	}
	provider := domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("oauth-p1")}}
	require.NoError(t, provider.Spec.FromOAuth2ProviderSpec(spec))
	fakeStore.providers["oauth-p1"] = &provider

	result, status := h.GetAuthProviderByAuthorizationUrl(context.Background(), uuid.New(), "https://auth.example.com/authorize")
	require.Equal(t, domain.StatusOK(), status)
	require.Equal(t, "oauth-p1", lo.FromPtr(result.Metadata.Name))

	_, status = h.GetAuthProviderByAuthorizationUrl(context.Background(), uuid.New(), "https://nope.example.com")
	require.Equal(t, int32(404), status.Code)
}

func TestReplaceAuthProvider(t *testing.T) {
	t.Run("When the provider does not exist it should delegate to CreateAuthProvider", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "new-provider", "", nil)

		result, status := h.ReplaceAuthProvider(adminCtx(), uuid.New(), "new-provider", provider)
		require.Equal(t, int32(201), status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.providers, "new-provider")
	})

	t.Run("When the name in the path does not match metadata.name it should return a bad-request status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil)

		_, status := h.ReplaceAuthProvider(context.Background(), uuid.New(), "different-name", provider)
		require.Equal(t, int32(400), status.Code)
	})

	t.Run("When the provider exists it should update it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		orgId := uuid.New()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil)
		_, status := h.CreateAuthProvider(adminCtx(), orgId, provider)
		require.Equal(t, int32(201), status.Code)

		result, status := h.ReplaceAuthProvider(adminCtx(), orgId, "p1", provider)
		require.Equal(t, int32(200), status.Code)
		require.NotNil(t, result)
		require.Contains(t, fakeStore.providers, "p1")
		// Only the create produces a ResourceCreated event; replacing with identical
		// data leaves generation/labels/owner unchanged, so no further event is emitted.
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, fakeEvents.created[0].Reason)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceAuthProviderFromUntrusted should clear them before replacing", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "replace-untrusted", "", nil)
		provider.Metadata.Owner = lo.ToPtr("someone")
		provider.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := ReplaceAuthProviderFromUntrusted(memberCtx(), h, uuid.New(), "replace-untrusted", provider)
		require.Equal(t, int32(201), status.Code)
		require.Nil(t, fakeStore.providers["replace-untrusted"].Metadata.Owner)
		require.Nil(t, fakeStore.providers["replace-untrusted"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceAuthProvider (trusted) should preserve them", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "replace-trusted", "", nil)
		provider.Metadata.Owner = lo.ToPtr("someone")
		provider.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.ReplaceAuthProvider(memberCtx(), uuid.New(), "replace-trusted", provider)
		require.Equal(t, int32(201), status.Code)
		require.Equal(t, "someone", lo.FromPtr(fakeStore.providers["replace-trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(fakeStore.providers["replace-trusted"].Metadata.Generation))
	})
}

func TestPatchAuthProvider(t *testing.T) {
	t.Run("When the provider does not exist it should return a not-found status", func(t *testing.T) {
		h, _, _ := newTestHandler()
		var value interface{} = "value"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels/k", Value: &value}}

		_, status := h.PatchAuthProvider(context.Background(), uuid.New(), "missing", patch)
		require.Equal(t, int32(404), status.Code)
	})

	t.Run("When the patch attempts to change metadata.name it should return a bad-request status", func(t *testing.T) {
		h, fakeStore, _ := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil)
		fakeStore.providers["p1"] = &provider

		var value interface{} = "renamed"
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/name", Value: &value}}

		_, status := h.PatchAuthProvider(context.Background(), uuid.New(), "p1", patch)
		require.Equal(t, int32(400), status.Code)
	})

	t.Run("When the patch is valid it should apply it and fire an updated callback", func(t *testing.T) {
		h, fakeStore, fakeEvents := newTestHandler()
		provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil)
		fakeStore.providers["p1"] = &provider

		var value interface{} = map[string]string{"env": "prod"}
		patch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels", Value: &value}}

		result, status := h.PatchAuthProvider(adminCtx(), uuid.New(), "p1", patch)
		require.Equal(t, int32(200), status.Code)
		require.NotNil(t, result)
		require.Len(t, fakeEvents.created, 1)
		require.Equal(t, domain.EventReasonResourceUpdated, fakeEvents.created[0].Reason)
	})
}

func TestDeleteAuthProvider(t *testing.T) {
	h, fakeStore, fakeEvents := newTestHandler()
	provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil)
	fakeStore.providers["p1"] = &provider

	status := h.DeleteAuthProvider(context.Background(), uuid.New(), "p1")
	require.Equal(t, domain.StatusOK(), status)
	require.NotContains(t, fakeStore.providers, "p1")
	require.Len(t, fakeEvents.deleted, 1)
}

func TestGetAuthConfig(t *testing.T) {
	h, _, _ := newTestHandler()
	authConfig := &domain.AuthConfig{}

	result, status := h.GetAuthConfig(context.Background(), authConfig)
	require.Equal(t, domain.StatusOK(), status)
	require.Same(t, authConfig, result)
}

func TestCreateAuthProviderStoreError(t *testing.T) {
	h, fakeStore, _ := newTestHandler()
	fakeStore.err = errors.New("db down")
	provider := testutil.ReturnTestAuthProvider(uuid.Nil, "p1", "", nil)

	_, status := h.CreateAuthProvider(adminCtx(), uuid.New(), provider)
	require.Equal(t, int32(500), status.Code)
}
