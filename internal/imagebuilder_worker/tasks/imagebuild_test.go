package tasks

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// mockStore is a mock implementation of store.Store for testing
type mockStore struct {
	repositories map[string]*v1beta1.Repository
}

func newMockStore() *mockStore {
	return &mockStore{
		repositories: make(map[string]*v1beta1.Repository),
	}
}

func (m *mockStore) Repository() store.Repository {
	return &mockRepositoryStore{store: m}
}

func (m *mockStore) Device() store.Device                                       { return nil }
func (m *mockStore) EnrollmentRequest() store.EnrollmentRequest                 { return nil }
func (m *mockStore) CertificateSigningRequest() store.CertificateSigningRequest { return nil }
func (m *mockStore) Fleet() store.Fleet                                         { return nil }
func (m *mockStore) TemplateVersion() store.TemplateVersion                     { return nil }
func (m *mockStore) ResourceSync() store.ResourceSync                           { return nil }
func (m *mockStore) Event() store.Event                                         { return nil }
func (m *mockStore) Checkpoint() store.Checkpoint                               { return nil }
func (m *mockStore) Organization() store.Organization                           { return nil }
func (m *mockStore) AuthProvider() store.AuthProvider                           { return nil }
func (m *mockStore) Catalog() store.Catalog                                     { return nil }
func (m *mockStore) RunMigrations(context.Context) error                        { return nil }
func (m *mockStore) CheckHealth(context.Context) error                          { return nil }
func (m *mockStore) Close() error                                               { return nil }

// mockRepositoryStore is a mock implementation of store.Repository
type mockRepositoryStore struct {
	store *mockStore
}

func (m *mockRepositoryStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*v1beta1.Repository, error) {
	repo, ok := m.store.repositories[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return repo, nil
}

func (m *mockRepositoryStore) InitialMigration(context.Context) error { return nil }
func (m *mockRepositoryStore) Create(context.Context, uuid.UUID, *v1beta1.Repository, store.EventCallback) (*v1beta1.Repository, error) {
	return nil, nil
}
func (m *mockRepositoryStore) Update(context.Context, uuid.UUID, *v1beta1.Repository, store.EventCallback) (*v1beta1.Repository, error) {
	return nil, nil
}
func (m *mockRepositoryStore) CreateOrUpdate(context.Context, uuid.UUID, *v1beta1.Repository, store.EventCallback) (*v1beta1.Repository, bool, error) {
	return nil, false, nil
}
func (m *mockRepositoryStore) List(context.Context, uuid.UUID, store.ListParams) (*v1beta1.RepositoryList, error) {
	return nil, nil
}
func (m *mockRepositoryStore) Delete(context.Context, uuid.UUID, string, store.EventCallback) error {
	return nil
}
func (m *mockRepositoryStore) UpdateStatus(context.Context, uuid.UUID, *v1beta1.Repository, store.EventCallback) (*v1beta1.Repository, error) {
	return nil, nil
}
func (m *mockRepositoryStore) GetFleetRefs(context.Context, uuid.UUID, string) (*v1beta1.FleetList, error) {
	return nil, nil
}
func (m *mockRepositoryStore) GetDeviceRefs(context.Context, uuid.UUID, string) (*v1beta1.DeviceList, error) {
	return nil, nil
}
func (m *mockRepositoryStore) Count(context.Context, uuid.UUID, store.ListParams) (int64, error) {
	return 0, nil
}
func (m *mockRepositoryStore) CountByOrg(context.Context, *uuid.UUID) ([]store.CountByOrgResult, error) {
	return nil, nil
}

// mockServiceHandler is a mock implementation of service.ServiceHandler for testing
type mockServiceHandler struct {
	enrollmentCredential *crypto.EnrollmentCredential
	generateError        error
}

func newMockServiceHandler() *mockServiceHandler {
	return &mockServiceHandler{}
}

func (m *mockServiceHandler) GenerateEnrollmentCredential(ctx context.Context, orgId uuid.UUID, baseName string, ownerKind string, ownerName string) (*crypto.EnrollmentCredential, v1beta1.Status) {
	if m.generateError != nil {
		return nil, v1beta1.StatusInternalServerError(m.generateError.Error())
	}
	if m.enrollmentCredential == nil {
		// Return a default mock credential
		return &crypto.EnrollmentCredential{
			CertificatePEM:       []byte("-----BEGIN CERTIFICATE-----\nMOCK_CERT\n-----END CERTIFICATE-----"),
			PrivateKeyPEM:        []byte("-----BEGIN PRIVATE KEY-----\nMOCK_KEY\n-----END PRIVATE KEY-----"),
			CABundlePEM:          []byte("-----BEGIN CERTIFICATE-----\nMOCK_CA\n-----END CERTIFICATE-----"),
			EnrollmentEndpoint:   "https://api.example.com",
			EnrollmentUIEndpoint: "https://ui.example.com",
			CSRName:              baseName + "-csr",
		}, v1beta1.StatusOK()
	}
	return m.enrollmentCredential, v1beta1.StatusOK()
}

func newTestImageBuild(name string, bindingType string) *api.ImageBuild {
	imageBuild := &api.ImageBuild{
		ApiVersion: api.ImageBuildAPIVersion,
		Kind:       string(api.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: api.ImageBuildSpec{
			Source: api.ImageBuildSource{
				Repository: "test-repo",
				ImageName:  "test-image",
				ImageTag:   "v1.0.0",
			},
			Destination: api.ImageBuildDestination{
				Repository: "output-repo",
				ImageName:  "output-image",
				ImageTag:   "v1.0.0",
			},
		},
	}

	if bindingType == "early" {
		binding := api.ImageBuildBinding{}
		_ = binding.FromEarlyBinding(api.EarlyBinding{
			Type: api.Early,
		})
		imageBuild.Spec.Binding = binding
	} else {
		binding := api.ImageBuildBinding{}
		_ = binding.FromLateBinding(api.LateBinding{
			Type: api.Late,
		})
		imageBuild.Spec.Binding = binding
	}

	return imageBuild
}

func createTestRepository(name string, registry string, scheme *v1beta1.OciRepoSpecScheme) *v1beta1.Repository {
	ociSpec := v1beta1.OciRepoSpec{
		Registry: registry,
		Type:     v1beta1.OciRepoSpecTypeOci,
		Scheme:   scheme,
	}
	spec := v1beta1.RepositorySpec{}
	_ = spec.FromOciRepoSpec(ociSpec)

	return &v1beta1.Repository{
		ApiVersion: v1beta1.RepositoryAPIVersion,
		Kind:       v1beta1.RepositoryKind,
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}
}

func TestContainerfileTemplate(t *testing.T) {
	// The Containerfile template is now static with ARG declarations
	// Values are passed via --build-arg at build time for safer execution
	require.NotEmpty(t, containerfileTemplate, "Containerfile template should not be empty")

	// Verify the template has required ARG declarations
	expectedArgs := []string{
		"ARG REGISTRY_HOSTNAME",
		"ARG IMAGE_NAME",
		"ARG IMAGE_TAG",
		"ARG EARLY_BINDING",
		"ARG HAS_USER_CONFIG",
		"ARG USERNAME",
		"ARG AGENT_CONFIG_DEST_PATH",
	}
	for _, arg := range expectedArgs {
		require.Contains(t, containerfileTemplate, arg, "Template should declare %q", arg)
	}

	// Verify FROM uses ARGs
	require.Contains(t, containerfileTemplate, "FROM ${REGISTRY_HOSTNAME}/${IMAGE_NAME}:${IMAGE_TAG}")

	// Verify COPY instructions for files
	require.Contains(t, containerfileTemplate, "COPY agent-config.yaml")
	require.Contains(t, containerfileTemplate, "COPY user-publickey.txt")

	// Verify conditional logic uses shell if statements
	require.Contains(t, containerfileTemplate, `if [ "${EARLY_BINDING}" = "true" ]`)
	require.Contains(t, containerfileTemplate, `if [ "${HAS_USER_CONFIG}" = "true" ]`)

	// Verify common content
	require.Contains(t, containerfileTemplate, "flightctl-agent")
	require.Contains(t, containerfileTemplate, "systemctl enable flightctl-agent.service")
}

func TestGenerateContainerfile_LateBinding(t *testing.T) {
	mockStore := newMockStore()
	mockStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	result, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, imageBuild, logger)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Containerfile)
	require.Nil(t, result.AgentConfig, "Late binding should not have agent config")

	// Verify BuildArgs are set correctly
	require.Equal(t, "quay.io", result.BuildArgs.RegistryHostname)
	require.Equal(t, "test-image", result.BuildArgs.ImageName)
	require.Equal(t, "v1.0.0", result.BuildArgs.ImageTag)
	require.False(t, result.BuildArgs.EarlyBinding)
	require.False(t, result.BuildArgs.HasUserConfig)

	// Verify Containerfile is static template with ARG declarations
	require.Contains(t, result.Containerfile, "ARG REGISTRY_HOSTNAME")
	require.Contains(t, result.Containerfile, "FROM ${REGISTRY_HOSTNAME}/${IMAGE_NAME}:${IMAGE_TAG}")
	require.Contains(t, result.Containerfile, "flightctl-agent")
}

func TestGenerateContainerfile_EarlyBinding(t *testing.T) {
	mockStore := newMockStore()
	mockStore.repositories["test-repo"] = createTestRepository("test-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "early")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	result, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, imageBuild, logger)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Containerfile)
	require.NotNil(t, result.AgentConfig, "Early binding should have agent config")
	require.NotEmpty(t, result.AgentConfig)

	// Verify BuildArgs are set correctly
	require.Equal(t, "registry.example.com", result.BuildArgs.RegistryHostname)
	require.Equal(t, "test-image", result.BuildArgs.ImageName)
	require.Equal(t, "v1.0.0", result.BuildArgs.ImageTag)
	require.True(t, result.BuildArgs.EarlyBinding)
	require.Equal(t, "/etc/flightctl/config.yaml", result.BuildArgs.AgentConfigDestPath)

	// Verify Containerfile is static template with ARG declarations
	require.Contains(t, result.Containerfile, "ARG EARLY_BINDING")
	require.Contains(t, result.Containerfile, "ARG AGENT_CONFIG_DEST_PATH")
	require.Contains(t, result.Containerfile, "flightctl-agent")
}

func TestGenerateContainerfile_RepositoryNotFound(t *testing.T) {
	mockStore := newMockStore()
	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	_, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, imageBuild, logger)

	require.Error(t, err)
	require.Contains(t, err.Error(), "repository")
	require.Contains(t, err.Error(), "not found")
}

func TestGenerateContainerfile_NilImageBuild(t *testing.T) {
	mockStore := newMockStore()
	mockServiceHandler := newMockServiceHandler()

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	_, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, nil, logger)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be nil")
}

func TestGenerateContainerfile_InvalidBindingType(t *testing.T) {
	mockStore := newMockStore()
	mockStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")

	// Create an invalid binding by setting it to an empty struct
	imageBuild.Spec.Binding = api.ImageBuildBinding{}

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	_, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, imageBuild, logger)

	require.Error(t, err)
	require.Contains(t, err.Error(), "binding type")
}

func TestGenerateContainerfile_ServiceHandlerError(t *testing.T) {
	mockStore := newMockStore()
	mockStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := &mockServiceHandler{
		generateError: fmt.Errorf("failed to generate credential"),
	}
	imageBuild := newTestImageBuild("test-build", "early")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	_, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, imageBuild, logger)

	require.Error(t, err)
	require.Contains(t, err.Error(), "agent config")
}

func TestGenerateContainerfile_WithUserConfiguration(t *testing.T) {
	mockStore := newMockStore()
	mockStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")
	testPublicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQAB test@example.com"
	imageBuild.Spec.UserConfiguration = &api.ImageBuildUserConfiguration{
		Username:  "testuser",
		Publickey: testPublicKey,
	}

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	result, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, imageBuild, logger)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Containerfile)

	// Verify BuildArgs are set correctly for user configuration
	require.True(t, result.BuildArgs.HasUserConfig)
	require.Equal(t, "testuser", result.BuildArgs.Username)

	// Verify Publickey is stored for writing to build context
	require.Equal(t, []byte(testPublicKey), result.Publickey)

	// Verify Containerfile has user configuration support (shell conditionals)
	require.Contains(t, result.Containerfile, "ARG HAS_USER_CONFIG")
	require.Contains(t, result.Containerfile, "ARG USERNAME")
	require.Contains(t, result.Containerfile, `if [ "${HAS_USER_CONFIG}" = "true" ]`)
}

func TestGenerateContainerfile_WithoutUserConfiguration(t *testing.T) {
	mockStore := newMockStore()
	mockStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")
	// No UserConfiguration set

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	result, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, imageBuild, logger)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Containerfile)

	// Verify BuildArgs indicate no user configuration
	require.False(t, result.BuildArgs.HasUserConfig)
	require.Empty(t, result.BuildArgs.Username)

	// Verify Publickey is nil when no user configuration
	require.Nil(t, result.Publickey)
}
