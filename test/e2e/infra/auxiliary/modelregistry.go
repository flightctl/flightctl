package auxiliary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// modelRegistryImage is pinned to the same upstream release as the
	// github.com/kubeflow/hub/pkg/openapi module the catalog collector is built
	// against. Tracking "latest" here would let an upstream release change the
	// REST contract under a green test run.
	modelRegistryImage         = "ghcr.io/kubeflow/hub/server:v0.3.10"
	modelRegistryContainerName = "e2e-model-registry"
	modelRegistryPort          = "8080/tcp"

	modelRegistryDBImage         = "docker.io/library/postgres:16"
	modelRegistryDBContainerName = "e2e-model-registry-db"
	modelRegistryDBPort          = "5432/tcp"
	modelRegistryDBName          = "model_registry"
	modelRegistryDBUser          = "postgres"
	modelRegistryDBPassword      = "e2e-model-registry-db-password" //nolint:gosec // G101: e2e test password only

	// modelRegistryNetworkName is a dedicated bridge so the server can resolve
	// its database by container name regardless of which network the rest of
	// the aux services use.
	modelRegistryNetworkName = "e2e-model-registry-net"

	// ModelRegistryAPIPath is the REST prefix the Model Registry serves and the
	// generated client appends on its own; the collector is configured with the
	// bare endpoint, while direct seeding calls need the full path.
	ModelRegistryAPIPath = "/api/model_registry/v1alpha3"

	modelRegistryDBStartupTimeout  = 2 * time.Minute
	modelRegistryAPIStartupTimeout = 3 * time.Minute
	modelRegistryReadyTimeout      = 2 * time.Minute
	modelRegistryPollInterval      = time.Second
	modelRegistryRequestTimeout    = 30 * time.Second
)

// ModelRegistry holds connection info and containers for the aux Kubeflow Model
// Registry service (REST server plus its PostgreSQL database).
//
// The service is not part of AllServices: it is only started on demand by the
// suites that need it (see StartServices with ServiceModelRegistry).
type ModelRegistry struct {
	URL          string
	Host         string
	Port         string
	InternalHost string
	InternalPort string

	networkName  string
	dbContainer  testcontainers.Container
	apiContainer testcontainers.Container
}

// Start starts the Model Registry containers (postgres + REST server).
func (m *ModelRegistry) Start(ctx context.Context, network string, reuse bool) error {
	logrus.Infof("Starting Kubeflow Model Registry containers (reuse=%v)", reuse)

	if err := m.ensureNetwork(ctx); err != nil {
		return fmt.Errorf("failed to create Model Registry network: %w", err)
	}
	if err := m.startDatabase(ctx, reuse); err != nil {
		return fmt.Errorf("failed to start Model Registry database: %w", err)
	}
	if err := m.startServer(ctx, network, reuse); err != nil {
		return fmt.Errorf("failed to start Model Registry server: %w", err)
	}
	if err := m.waitForAPI(ctx); err != nil {
		return fmt.Errorf("model registry API not ready: %w", err)
	}

	logrus.Infof("Kubeflow Model Registry started: %s", m.URL)
	return nil
}

func (m *ModelRegistry) ensureNetwork(ctx context.Context) error {
	m.networkName = modelRegistryNetworkName

	cli := containerRuntimeCLIName()
	if err := exec.CommandContext(ctx, cli, "network", "inspect", m.networkName).Run(); err == nil {
		logrus.Infof("Model Registry network %s already exists", m.networkName)
		return nil
	}

	logrus.Infof("Creating Model Registry internal network: %s", m.networkName)
	if err := exec.CommandContext(ctx, cli, "network", "create", m.networkName).Run(); err != nil {
		return fmt.Errorf("failed to create network %s: %w", m.networkName, err)
	}
	return nil
}

func (m *ModelRegistry) startDatabase(ctx context.Context, reuse bool) error {
	req := testcontainers.ContainerRequest{
		Image:        modelRegistryDBImage,
		Name:         modelRegistryDBContainerName,
		ExposedPorts: []string{modelRegistryDBPort},
		Env: map[string]string{
			"POSTGRES_DB":       modelRegistryDBName,
			"POSTGRES_USER":     modelRegistryDBUser,
			"POSTGRES_PASSWORD": modelRegistryDBPassword,
			"PGDATA":            "/var/lib/postgresql/data",
		},
		WaitingFor: wait.ForExec([]string{"pg_isready", "-U", modelRegistryDBUser, "-d", modelRegistryDBName}).
			WithStartupTimeout(modelRegistryDBStartupTimeout),
		SkipReaper: reuse,
	}

	container, err := CreateContainer(ctx, req, reuse, WithNetwork(m.networkName), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start Model Registry DB container: %w", err)
	}
	m.dbContainer = container
	logrus.Info("Model Registry postgres container started")
	return nil
}

func (m *ModelRegistry) startServer(ctx context.Context, externalNetwork string, reuse bool) error {
	// The server runs the EmbedMD datastore, so it talks to PostgreSQL directly
	// and needs no separate MLMD deployment.
	dsn := fmt.Sprintf(
		"host=%s port=5432 user=%s password=%s dbname=%s sslmode=disable",
		modelRegistryDBContainerName, modelRegistryDBUser, modelRegistryDBPassword, modelRegistryDBName,
	)

	req := testcontainers.ContainerRequest{
		Image:        modelRegistryImage,
		Name:         modelRegistryContainerName,
		ExposedPorts: []string{modelRegistryPort},
		Cmd: []string{
			"proxy",
			"--hostname", "0.0.0.0",
			"--datastore-type", "embedmd",
			"--embedmd-database-type", "postgres",
			"--embedmd-database-dsn", dsn,
		},
		WaitingFor: wait.ForHTTP(ModelRegistryAPIPath + "/registered_models").
			WithPort("8080/tcp").
			WithStartupTimeout(modelRegistryAPIStartupTimeout),
		SkipReaper: reuse,
	}

	container, err := CreateContainer(ctx, req, reuse, WithNetwork(m.networkName), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start Model Registry server container: %w", err)
	}
	m.apiContainer = container

	// Join the shared aux network as well so container-to-container access works
	// the same way it does for the other aux services.
	if externalNetwork != "" && externalNetwork != m.networkName {
		//nolint:gosec // G204: runtime CLI name and container name are package constants; externalNetwork comes from the aux network config
		cmd := exec.CommandContext(ctx, containerRuntimeCLIName(), "network", "connect", externalNetwork, modelRegistryContainerName)
		if err := cmd.Run(); err != nil {
			logrus.Warnf("Failed to connect Model Registry container to external network %s: %v", externalNetwork, err)
		}
	}

	m.Host = GetHostIP()
	port, err := container.MappedPort(ctx, "8080")
	if err != nil {
		return fmt.Errorf("failed to get Model Registry mapped port: %w", err)
	}
	m.Port = port.Port()
	m.URL = fmt.Sprintf("http://%s", net.JoinHostPort(m.Host, m.Port))
	m.InternalHost = modelRegistryContainerName
	m.InternalPort = "8080"
	return nil
}

// waitForAPI polls the host-mapped endpoint. The container wait strategy only
// proves the port answers inside the container network; reuse skips it entirely,
// so the host path is verified here before any caller seeds data.
func (m *ModelRegistry) waitForAPI(ctx context.Context) error {
	url := m.URL + ModelRegistryAPIPath + "/registered_models?pageSize=1"
	client := &http.Client{Timeout: modelRegistryRequestTimeout}
	deadline := time.Now().Add(modelRegistryReadyTimeout)

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("build Model Registry readiness request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for Model Registry API: %w", ctx.Err())
		case <-time.After(modelRegistryPollInterval):
		}
	}
	return fmt.Errorf("model registry API not reachable at %s after %s", url, modelRegistryReadyTimeout)
}

// InternalURL returns the URL for container-to-container access.
func (m *ModelRegistry) InternalURL() string {
	return fmt.Sprintf("http://%s", net.JoinHostPort(m.InternalHost, m.InternalPort))
}

// RegisteredModel is the subset of the Model Registry RegisteredModel resource
// the e2e suites create and assert on.
type RegisteredModel struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner,omitempty"`
	State       string `json:"state,omitempty"`
}

// ModelVersion is the subset of the Model Registry ModelVersion resource the
// e2e suites create and assert on.
type ModelVersion struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"name"`
	Author            string `json:"author,omitempty"`
	RegisteredModelID string `json:"registeredModelId,omitempty"`
	State             string `json:"state,omitempty"`
}

// ModelArtifact is the subset of the Model Registry ModelArtifact resource the
// e2e suites create and assert on.
type ModelArtifact struct {
	ID                 string `json:"id,omitempty"`
	Name               string `json:"name"`
	ArtifactType       string `json:"artifactType,omitempty"`
	URI                string `json:"uri,omitempty"`
	State              string `json:"state,omitempty"`
	ModelFormatName    string `json:"modelFormatName,omitempty"`
	ModelFormatVersion string `json:"modelFormatVersion,omitempty"`
}

// Model Registry lifecycle states used by the e2e suites. The collector selects
// LIVE resources by default; ARCHIVED is how a seeded resource is retired,
// because the REST API exposes no delete for registered models or versions.
const (
	ModelStateLive     = "LIVE"
	ModelStateArchived = "ARCHIVED"
)

// CreateRegisteredModel creates a LIVE registered model and returns it with the
// server-assigned ID.
func (m *ModelRegistry) CreateRegisteredModel(ctx context.Context, model RegisteredModel) (*RegisteredModel, error) {
	if model.State == "" {
		model.State = ModelStateLive
	}
	created := &RegisteredModel{}
	if err := m.do(ctx, http.MethodPost, "/registered_models", model, created); err != nil {
		return nil, fmt.Errorf("create registered model %q: %w", model.Name, err)
	}
	if created.ID == "" {
		return nil, fmt.Errorf("create registered model %q: server returned no id", model.Name)
	}
	return created, nil
}

// CreateModelVersion creates a LIVE version of a registered model and returns it
// with the server-assigned ID.
func (m *ModelRegistry) CreateModelVersion(ctx context.Context, modelID string, version ModelVersion) (*ModelVersion, error) {
	if modelID == "" {
		return nil, fmt.Errorf("create model version %q: registered model id is required", version.Name)
	}
	if version.State == "" {
		version.State = ModelStateLive
	}
	// The server rejects the request when the body omits the parent id, even
	// though it is already present in the path.
	version.RegisteredModelID = modelID

	created := &ModelVersion{}
	path := fmt.Sprintf("/registered_models/%s/versions", modelID)
	if err := m.do(ctx, http.MethodPost, path, version, created); err != nil {
		return nil, fmt.Errorf("create model version %q: %w", version.Name, err)
	}
	if created.ID == "" {
		return nil, fmt.Errorf("create model version %q: server returned no id", version.Name)
	}
	return created, nil
}

// CreateModelArtifact attaches a LIVE model artifact to a model version and
// returns it with the server-assigned ID.
func (m *ModelRegistry) CreateModelArtifact(ctx context.Context, versionID string, artifact ModelArtifact) (*ModelArtifact, error) {
	if versionID == "" {
		return nil, fmt.Errorf("create model artifact %q: model version id is required", artifact.Name)
	}
	if artifact.State == "" {
		artifact.State = ModelStateLive
	}
	if artifact.ArtifactType == "" {
		artifact.ArtifactType = "model-artifact"
	}

	created := &ModelArtifact{}
	path := fmt.Sprintf("/model_versions/%s/artifacts", versionID)
	if err := m.do(ctx, http.MethodPost, path, artifact, created); err != nil {
		return nil, fmt.Errorf("create model artifact %q: %w", artifact.Name, err)
	}
	if created.ID == "" {
		return nil, fmt.Errorf("create model artifact %q: server returned no id", artifact.Name)
	}
	return created, nil
}

// ArchiveRegisteredModel moves a registered model out of the LIVE selection the
// collector imports. The REST API has no delete, so archiving is how a suite
// retires the data it seeded.
func (m *ModelRegistry) ArchiveRegisteredModel(ctx context.Context, modelID string) error {
	if modelID == "" {
		return fmt.Errorf("archive registered model: id is required")
	}
	body := map[string]string{"state": ModelStateArchived}
	if err := m.do(ctx, http.MethodPatch, "/registered_models/"+modelID, body, nil); err != nil {
		return fmt.Errorf("archive registered model %s: %w", modelID, err)
	}
	return nil
}

// ArchiveModelVersion moves a model version out of the LIVE selection the
// collector imports.
func (m *ModelRegistry) ArchiveModelVersion(ctx context.Context, versionID string) error {
	if versionID == "" {
		return fmt.Errorf("archive model version: id is required")
	}
	body := map[string]string{"state": ModelStateArchived}
	if err := m.do(ctx, http.MethodPatch, "/model_versions/"+versionID, body, nil); err != nil {
		return fmt.Errorf("archive model version %s: %w", versionID, err)
	}
	return nil
}

// do performs one bounded Model Registry REST call. out may be nil when the
// response body is not needed.
func (m *ModelRegistry) do(ctx context.Context, method, path string, payload any, out any) error {
	if m.URL == "" {
		return fmt.Errorf("model registry is not started")
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	url := m.URL + ModelRegistryAPIPath + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: modelRegistryRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close error is not actionable here

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: unexpected status %s: %s", method, path, resp.Status, strings.TrimSpace(string(responseBody)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// Stop terminates the Model Registry containers and removes the internal network.
func (m *ModelRegistry) Stop(ctx context.Context) error {
	var errs []error

	if m.apiContainer != nil {
		if err := m.apiContainer.Terminate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop Model Registry server: %w", err))
		}
	}
	if m.dbContainer != nil {
		if err := m.dbContainer.Terminate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop Model Registry DB: %w", err))
		}
	}
	if m.networkName != "" {
		cmd := exec.CommandContext(ctx, containerRuntimeCLIName(), "network", "rm", m.networkName) //nolint:gosec // G204: fixed e2e aux network name
		if err := cmd.Run(); err != nil {
			logrus.Warnf("Failed to remove Model Registry network %s: %v", m.networkName, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors stopping Model Registry: %v", errs)
	}
	return nil
}

// StopModelRegistryContainers force-removes the Model Registry containers and
// network by name. Used by StopServices when no live instance is available.
func StopModelRegistryContainers() {
	for _, name := range []string{modelRegistryContainerName, modelRegistryDBContainerName} {
		logrus.Infof("Stopping Model Registry container %s", name)
		if err := podmanRemove(name); err != nil {
			logrus.Warnf("Could not remove %s: %v", name, err)
		}
	}

	logrus.Infof("Removing Model Registry network %s", modelRegistryNetworkName)
	//nolint:gosec // G204: runtime CLI name and network name are package constants
	cmd := exec.Command(containerRuntimeCLIName(), "network", "rm", modelRegistryNetworkName)
	if err := cmd.Run(); err != nil {
		logrus.Warnf("Could not remove network %s: %v", modelRegistryNetworkName, err)
	}
}
