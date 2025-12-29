package tasks

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
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
		Kind:       api.ImageBuildKind,
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
				Tag:        "v1.0.0",
			},
		},
	}

	if bindingType == "early" {
		binding := api.ImageBuildBinding{}
		_ = binding.FromEarlyBinding(api.EarlyBinding{
			Type:     api.Early,
			CertName: "test-cert",
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
		Type:     v1beta1.RepoSpecTypeOci,
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

func TestRenderContainerfileTemplate(t *testing.T) {
	tests := []struct {
		name     string
		data     containerfileData
		expected []string // strings that must be present in the output
	}{
		{
			name: "late binding template",
			data: containerfileData{
				RegistryURL:         "quay.io",
				ImageName:           "test-image",
				ImageTag:            "v1.0.0",
				EarlyBinding:        false,
				AgentConfigDestPath: "/etc/flightctl/config.yaml",
				HeredocDelimiter:    "FLIGHTCTL_CONFIG_ABC123",
			},
			expected: []string{
				"FROM quay.io/test-image:v1.0.0",
				"flightctl-agent",
				"systemctl enable flightctl-agent.service",
				"ignition",
			},
		},
		{
			name: "early binding template",
			data: containerfileData{
				RegistryURL:         "registry.example.com",
				ImageName:           "base-image",
				ImageTag:            "latest",
				EarlyBinding:        true,
				AgentConfig:         "test: config\ndata: value",
				AgentConfigDestPath: "/etc/flightctl/config.yaml",
				HeredocDelimiter:    "FLIGHTCTL_CONFIG_XYZ789",
			},
			expected: []string{
				"FROM registry.example.com/base-image:latest",
				"flightctl-agent",
				"systemctl enable flightctl-agent.service",
				"/etc/flightctl/config.yaml",
				"FLIGHTCTL_CONFIG_XYZ789",
				"test: config",
				"chmod 600",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := renderContainerfileTemplate(tt.data)
			require.NoError(t, err)
			require.NotEmpty(t, result)

			for _, expected := range tt.expected {
				require.Contains(t, result, expected, "Containerfile should contain %q", expected)
			}

			// Verify early binding doesn't include ignition
			if tt.data.EarlyBinding {
				require.NotContains(t, result, "ignition", "Early binding should not include ignition")
			} else {
				require.NotContains(t, result, "FLIGHTCTL_CONFIG", "Late binding should not include agent config")
			}
		})
	}
}

func TestGetRepositoryURL(t *testing.T) {
	tests := []struct {
		name          string
		repoName      string
		repository    *v1beta1.Repository
		expectedURL   string
		expectedError bool
	}{
		{
			name:        "OCI repository without scheme",
			repoName:    "test-repo",
			repository:  createTestRepository("test-repo", "quay.io", nil),
			expectedURL: "quay.io",
		},
		{
			name:        "OCI repository with https scheme",
			repoName:    "test-repo",
			repository:  createTestRepository("test-repo", "registry.example.com", lo.ToPtr(v1beta1.OciRepoSpecSchemeHttps)),
			expectedURL: "https://registry.example.com",
		},
		{
			name:        "OCI repository with http scheme",
			repoName:    "test-repo",
			repository:  createTestRepository("test-repo", "localhost:5000", lo.ToPtr(v1beta1.OciRepoSpecSchemeHttp)),
			expectedURL: "http://localhost:5000",
		},
		{
			name:          "repository not found",
			repoName:      "nonexistent",
			repository:    nil,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := newMockStore()
			if tt.repository != nil {
				mockStore.repositories[tt.repoName] = tt.repository
			}

			ctx := context.Background()
			orgID := uuid.New()

			url, err := getRepositoryURL(ctx, mockStore, orgID, tt.repoName)

			if tt.expectedError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedURL, url)
			}
		})
	}
}

func TestGetRepositoryURL_NonOCIRepository(t *testing.T) {
	mockStore := newMockStore()

	// Create a Git repository instead of OCI
	gitSpec := v1beta1.RepositorySpec{}
	_ = gitSpec.FromGenericRepoSpec(v1beta1.GenericRepoSpec{
		Type: v1beta1.RepoSpecTypeGit,
		Url:  "https://github.com/example/repo.git",
	})
	gitRepo := &v1beta1.Repository{
		ApiVersion: v1beta1.RepositoryAPIVersion,
		Kind:       v1beta1.RepositoryKind,
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr("git-repo"),
		},
		Spec: gitSpec,
	}
	mockStore.repositories["git-repo"] = gitRepo

	ctx := context.Background()
	orgID := uuid.New()

	_, err := getRepositoryURL(ctx, mockStore, orgID, "git-repo")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be of type 'oci'")
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

	// Verify Containerfile content
	require.Contains(t, result.Containerfile, "FROM quay.io/test-image:v1.0.0")
	require.Contains(t, result.Containerfile, "flightctl-agent")
	require.Contains(t, result.Containerfile, "ignition")
	require.NotContains(t, result.Containerfile, "FLIGHTCTL_CONFIG", "Late binding should not include agent config")
}

func TestGenerateContainerfile_EarlyBinding(t *testing.T) {
	mockStore := newMockStore()
	mockStore.repositories["test-repo"] = createTestRepository("test-repo", "registry.example.com", lo.ToPtr(v1beta1.OciRepoSpecSchemeHttps))

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

	// Verify Containerfile content
	require.Contains(t, result.Containerfile, "FROM https://registry.example.com/test-image:v1.0.0")
	require.Contains(t, result.Containerfile, "flightctl-agent")
	require.Contains(t, result.Containerfile, "/etc/flightctl/config.yaml")
	require.Contains(t, result.Containerfile, "chmod 600")
	require.NotContains(t, result.Containerfile, "ignition", "Early binding should not include ignition")
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

func TestGenerateContainerfile_HeredocDelimiterUniqueness(t *testing.T) {
	mockStore := newMockStore()
	mockStore.repositories["test-repo"] = createTestRepository("test-repo", "quay.io", nil)

	mockServiceHandler := newMockServiceHandler()
	imageBuild := newTestImageBuild("test-build", "early")

	ctx := context.Background()
	orgID := uuid.New()
	logger := log.InitLogs()

	// Generate multiple containerfiles and verify heredoc delimiters are unique
	delimiters := make(map[string]bool)
	for i := 0; i < 10; i++ {
		result, err := GenerateContainerfile(ctx, mockStore, mockServiceHandler, orgID, imageBuild, logger)
		require.NoError(t, err)

		// Extract heredoc delimiter from the Containerfile
		lines := strings.Split(result.Containerfile, "\n")
		for _, line := range lines {
			if strings.Contains(line, "FLIGHTCTL_CONFIG_") {
				parts := strings.Fields(line)
				for _, part := range parts {
					if strings.HasPrefix(part, "FLIGHTCTL_CONFIG_") {
						delimiter := strings.Trim(part, "'")
						require.False(t, delimiters[delimiter], "Heredoc delimiter should be unique: %s", delimiter)
						delimiters[delimiter] = true
						break
					}
				}
			}
		}
	}
}
