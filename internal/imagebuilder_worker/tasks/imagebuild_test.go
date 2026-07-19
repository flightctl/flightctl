package tasks

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// mockRepositoryStore is a mock implementation of RepositoryLookup for testing
type mockRepositoryStore struct {
	repositories map[string]*domain.Repository
}

func newMockRepositoryStore() *mockRepositoryStore {
	return &mockRepositoryStore{
		repositories: make(map[string]*domain.Repository),
	}
}

func (m *mockRepositoryStore) GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, domain.Status) {
	repo, ok := m.repositories[name]
	if !ok {
		return nil, domain.StatusResourceNotFound(domain.RepositoryKind, name)
	}
	return repo, domain.StatusOK()
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
	repoStore := newMockRepositoryStore()
	repoStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	result, err := GenerateContainerfile(ctx, repoStore, mockServiceHandler, orgID, imageBuild, logger)

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
	repoStore := newMockRepositoryStore()
	repoStore.repositories["test-repo"] = createTestRepository("test-repo", "registry.example.com", lo.ToPtr(v1beta1.Https))

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "early")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	result, err := GenerateContainerfile(ctx, repoStore, mockServiceHandler, orgID, imageBuild, logger)

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
	repoStore := newMockRepositoryStore()
	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	_, err := GenerateContainerfile(ctx, repoStore, mockServiceHandler, orgID, imageBuild, logger)

	require.Error(t, err)
	require.Contains(t, err.Error(), "repository")
	require.Contains(t, err.Error(), "not found")
}

func TestGenerateContainerfile_NilImageBuild(t *testing.T) {
	repoStore := newMockRepositoryStore()
	mockServiceHandler := newMockServiceHandler()

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	_, err := GenerateContainerfile(ctx, repoStore, mockServiceHandler, orgID, nil, logger)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be nil")
}

func TestGenerateContainerfile_InvalidBindingType(t *testing.T) {
	repoStore := newMockRepositoryStore()
	repoStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")

	// Create an invalid binding by setting it to an empty struct
	imageBuild.Spec.Binding = api.ImageBuildBinding{}

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	_, err := GenerateContainerfile(ctx, repoStore, mockServiceHandler, orgID, imageBuild, logger)

	require.Error(t, err)
	require.Contains(t, err.Error(), "binding type")
}

func TestGenerateContainerfile_ServiceHandlerError(t *testing.T) {
	repoStore := newMockRepositoryStore()
	repoStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := &mockServiceHandler{
		generateError: fmt.Errorf("failed to generate credential"),
	}
	imageBuild := newTestImageBuild("test-build", "early")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	_, err := GenerateContainerfile(ctx, repoStore, mockServiceHandler, orgID, imageBuild, logger)

	require.Error(t, err)
	require.Contains(t, err.Error(), "agent config")
}

func TestGenerateContainerfile_WithUserConfiguration(t *testing.T) {
	repoStore := newMockRepositoryStore()
	repoStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

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

	result, err := GenerateContainerfile(ctx, repoStore, mockServiceHandler, orgID, imageBuild, logger)

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
	repoStore := newMockRepositoryStore()
	repoStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "late")
	// No UserConfiguration set

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	result, err := GenerateContainerfile(ctx, repoStore, mockServiceHandler, orgID, imageBuild, logger)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Containerfile)

	// Verify BuildArgs indicate no user configuration
	require.False(t, result.BuildArgs.HasUserConfig)
	require.Empty(t, result.BuildArgs.Username)

	// Verify Publickey is nil when no user configuration
	require.Nil(t, result.Publickey)
}

func TestInstallCACertInWorker_NilCaCrt(t *testing.T) {
	err := installCACertInWorker(context.Background(), nil, "fake-container", "registry.example.com", log.InitLogs())
	require.NoError(t, err)
}

func TestInstallCACertInWorker_EmptyCaCrt(t *testing.T) {
	empty := ""
	err := installCACertInWorker(context.Background(), &empty, "fake-container", "registry.example.com", log.InitLogs())
	require.NoError(t, err)
}

func TestInstallCACertInWorker_InvalidBase64(t *testing.T) {
	invalid := "not-valid-base64!!!"
	err := installCACertInWorker(context.Background(), &invalid, "fake-container", "registry.example.com", log.InitLogs())
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to decode CA certificate")
}

func TestInstallCACertInWorker_ValidBase64FailsWithoutContainer(t *testing.T) {
	caPEM := "-----BEGIN CERTIFICATE-----\nTESTCA\n-----END CERTIFICATE-----"
	encoded := base64.StdEncoding.EncodeToString([]byte(caPEM))
	err := installCACertInWorker(context.Background(), &encoded, "nonexistent-container", "registry.example.com", log.InitLogs())
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to create cert dir in container")
}
