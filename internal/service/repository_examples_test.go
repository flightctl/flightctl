package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// getExamplesDir returns the path to the examples directory
func getExamplesDir() string {
	_, filename, _, _ := runtime.Caller(0)
	// Navigate from internal/service to project root and then to examples
	return filepath.Join(filepath.Dir(filename), "..", "..", "examples")
}

// loadRepositoryFromYAML loads a Repository from a YAML file
// It uses a two-step process: YAML -> map -> JSON -> struct
// This ensures that the RepositorySpec union type is properly populated
func loadRepositoryFromYAML(filename string) (*domain.Repository, error) {
	examplesDir := getExamplesDir()
	yamlPath := filepath.Join(examplesDir, filename)

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}

	// First unmarshal YAML into a generic map
	var yamlMap map[string]interface{}
	if err := yaml.Unmarshal(data, &yamlMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	// Convert map to JSON
	jsonData, err := json.Marshal(yamlMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal to JSON: %w", err)
	}

	// Unmarshal JSON into the struct (this triggers proper UnmarshalJSON for union types)
	var repo domain.Repository
	if err := json.Unmarshal(jsonData, &repo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return &repo, nil
}

// validateRepositoryAgainstOpenAPI validates a Repository against the OpenAPI schema
// This simulates the middleware validation that happens when a request is sent to the API
func validateRepositoryAgainstOpenAPI(ctx context.Context, repo *domain.Repository) error {
	// Get the OpenAPI spec
	swagger, err := domain.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed to get swagger spec: %w", err)
	}
	// Skip server name validation
	swagger.Servers = nil

	// Marshal the repository to JSON
	jsonData, err := json.Marshal(repo)
	if err != nil {
		return fmt.Errorf("failed to marshal repository to JSON: %w", err)
	}

	// Create a fake HTTP request for the PUT /api/v1/repositories/{name} endpoint
	repoName := "test-repo"
	if repo.Metadata.Name != nil {
		repoName = *repo.Metadata.Name
	}
	reqURL, err := url.Parse(fmt.Sprintf("/api/v1/repositories/%s", repoName))
	if err != nil {
		return fmt.Errorf("failed to parse URL: %w", err)
	}

	httpReq := &http.Request{
		Method: "PUT",
		URL:    reqURL,
		Body:   io.NopCloser(bytes.NewReader(jsonData)),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	// Create router from swagger spec
	router, err := gorillamux.NewRouter(swagger)
	if err != nil {
		return fmt.Errorf("failed to create router: %w", err)
	}

	// Find the route for this request
	route, pathParams, err := router.FindRoute(httpReq)
	if err != nil {
		return fmt.Errorf("failed to find route: %w", err)
	}

	// Validate the request against the OpenAPI schema
	requestValidationInput := &openapi3filter.RequestValidationInput{
		Request:    httpReq,
		PathParams: pathParams,
		Route:      route,
	}
	if err := openapi3filter.ValidateRequest(ctx, requestValidationInput); err != nil {
		return fmt.Errorf("OpenAPI validation failed: %w", err)
	}

	return nil
}

// createTestServiceHandler creates a ServiceHandler with a TestStore for testing
func createTestServiceHandler() ServiceHandler {
	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	return ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, logrus.New()),
		store:        ts,
		workerClient: wc,
		log:          logrus.New(),
	}
}

func TestRepositoryExampleFlightctl(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	repo, err := loadRepositoryFromYAML("repository-flightctl.yaml")
	require.NoError(err)
	require.NotNil(repo)
	require.NotNil(repo.Metadata.Name)

	// Validate against OpenAPI schema (same validation as middleware)
	err = validateRepositoryAgainstOpenAPI(ctx, repo)
	require.NoError(err, "OpenAPI validation should pass for repository-flightctl.yaml")

	// Create via service layer (includes service layer validation)
	serviceHandler := createTestServiceHandler()
	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, *repo)
	require.Equal(int32(201), status.Code, "CreateRepository should return 201")
	require.NotNil(resp)
	require.Equal(*repo.Metadata.Name, *resp.Metadata.Name)

	// Verify we can retrieve it
	retrieved, err := serviceHandler.store.Repository().Get(ctx, store.NullOrgId, *repo.Metadata.Name)
	require.NoError(err)
	require.NotNil(retrieved)

	// Verify spec type
	specType, err := retrieved.Spec.Discriminator()
	require.NoError(err)
	require.Equal("git", specType)

	// Verify it can be decoded as GenericRepoSpec
	genericSpec, err := retrieved.Spec.GetGenericRepoSpec()
	require.NoError(err)
	require.Equal("https://github.com/flightctl/flightctl.git", genericSpec.Url)
	require.Equal(domain.RepoSpecTypeGit, genericSpec.Type)
}

func TestRepositoryExampleSsh(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	repo, err := loadRepositoryFromYAML("repository-ssh.yaml")
	require.NoError(err)
	require.NotNil(repo)
	require.NotNil(repo.Metadata.Name)

	// Validate against OpenAPI schema (same validation as middleware)
	err = validateRepositoryAgainstOpenAPI(ctx, repo)
	require.NoError(err, "OpenAPI validation should pass for repository-ssh.yaml")

	// Create via service layer (includes service layer validation)
	serviceHandler := createTestServiceHandler()
	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, *repo)
	require.Equal(int32(201), status.Code, "CreateRepository should return 201")
	require.NotNil(resp)
	require.Equal(*repo.Metadata.Name, *resp.Metadata.Name)

	// Verify we can retrieve it
	retrieved, err := serviceHandler.store.Repository().Get(ctx, store.NullOrgId, *repo.Metadata.Name)
	require.NoError(err)
	require.NotNil(retrieved)

	// Verify spec type
	specType, err := retrieved.Spec.Discriminator()
	require.NoError(err)
	require.Equal("git", specType)

	// Verify it can be decoded as SshRepoSpec
	sshSpec, err := retrieved.Spec.GetSshRepoSpec()
	require.NoError(err)
	require.Equal("ssh://git@github.com/flightctl/flightctl.git", sshSpec.Url)
	require.Equal(domain.RepoSpecTypeGit, sshSpec.Type)
	require.NotNil(sshSpec.SshConfig.SshPrivateKey)
	require.NotNil(sshSpec.SshConfig.PrivateKeyPassphrase)
	require.Equal("testpassphrase", *sshSpec.SshConfig.PrivateKeyPassphrase)
	require.NotNil(sshSpec.SshConfig.SkipServerVerification)
	require.True(*sshSpec.SshConfig.SkipServerVerification)
}

func TestRepositoryExampleHttp(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	repo, err := loadRepositoryFromYAML("repository-http.yaml")
	require.NoError(err)
	require.NotNil(repo)
	require.NotNil(repo.Metadata.Name)

	// Validate against OpenAPI schema (same validation as middleware)
	err = validateRepositoryAgainstOpenAPI(ctx, repo)
	require.NoError(err, "OpenAPI validation should pass for repository-http.yaml")

	// Create via service layer (includes service layer validation)
	serviceHandler := createTestServiceHandler()
	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, *repo)
	require.Equal(int32(201), status.Code, "CreateRepository should return 201")
	require.NotNil(resp)
	require.Equal(*repo.Metadata.Name, *resp.Metadata.Name)

	// Verify we can retrieve it
	retrieved, err := serviceHandler.store.Repository().Get(ctx, store.NullOrgId, *repo.Metadata.Name)
	require.NoError(err)
	require.NotNil(retrieved)

	// Verify spec type
	specType, err := retrieved.Spec.Discriminator()
	require.NoError(err)
	require.Equal("http", specType)

	// Verify it can be decoded as HttpRepoSpec
	httpSpec, err := retrieved.Spec.GetHttpRepoSpec()
	require.NoError(err)
	require.Equal("https://my-server.com/flightctl", httpSpec.Url)
	require.Equal(domain.RepoSpecTypeHttp, httpSpec.Type)
	require.NotNil(httpSpec.HttpConfig.Username)
	require.Equal("myusername", *httpSpec.HttpConfig.Username)
	require.NotNil(httpSpec.HttpConfig.Password)
	require.Equal("mypassword", *httpSpec.HttpConfig.Password)
	require.NotNil(httpSpec.HttpConfig.SkipServerVerification)
	require.True(*httpSpec.HttpConfig.SkipServerVerification)
}

func TestRepositoryExampleHttpGit(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	repo, err := loadRepositoryFromYAML("repository-http-git.yaml")
	require.NoError(err)
	require.NotNil(repo)
	require.NotNil(repo.Metadata.Name)

	// Validate against OpenAPI schema (same validation as middleware)
	err = validateRepositoryAgainstOpenAPI(ctx, repo)
	require.NoError(err, "OpenAPI validation should pass for repository-http-git.yaml")

	// Create via service layer (includes service layer validation)
	serviceHandler := createTestServiceHandler()
	resp, status := serviceHandler.CreateRepository(ctx, store.NullOrgId, *repo)
	require.Equal(int32(201), status.Code, "CreateRepository should return 201")
	require.NotNil(resp)
	require.Equal(*repo.Metadata.Name, *resp.Metadata.Name)

	// Verify we can retrieve it
	retrieved, err := serviceHandler.store.Repository().Get(ctx, store.NullOrgId, *repo.Metadata.Name)
	require.NoError(err)
	require.NotNil(retrieved)

	// Verify spec type
	specType, err := retrieved.Spec.Discriminator()
	require.NoError(err)
	require.Equal("http", specType)

	// Verify it can be decoded as HttpRepoSpec
	httpSpec, err := retrieved.Spec.GetHttpRepoSpec()
	require.NoError(err)
	require.Equal("https://github.com/flightctl/flightctl.git", httpSpec.Url)
	require.Equal(domain.RepoSpecTypeHttp, httpSpec.Type)
	require.NotNil(httpSpec.HttpConfig.Username)
	require.Equal("myusername", *httpSpec.HttpConfig.Username)
	require.NotNil(httpSpec.HttpConfig.Password)
	require.Equal("mypassword", *httpSpec.HttpConfig.Password)
	require.NotNil(httpSpec.HttpConfig.SkipServerVerification)
	require.True(*httpSpec.HttpConfig.SkipServerVerification)
}
