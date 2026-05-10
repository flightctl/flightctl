package auxiliary

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	trustifyDBImage     = "docker.io/library/postgres:17-alpine"
	trustifyDBContainer = "e2e-trustify-db"
	trustifyDBPort      = "5432/tcp"
	trustifyDBName      = "trustify"
	trustifyDBUser      = "postgres"
	trustifyDBPassword  = "password" //nolint:gosec // G101: e2e test password only

	trustifyAPIImage          = "ghcr.io/guacsec/trustd:0.4.11"
	trustifyAPIContainer      = "e2e-trustify-api"
	trustifyAPIPort           = "8080/tcp"
	trustifyImporterImage     = "ghcr.io/guacsec/trustd:0.4.11"
	trustifyImporterContainer = "e2e-trustify-importer"

	trustifyNetworkName = "e2e-trustify-net"
)

// Trustify holds connection info and containers for the aux Trustify service.
type Trustify struct {
	URL               string
	Host              string
	Port              string
	InternalHost      string
	InternalPort      string
	networkName       string
	dbContainer       testcontainers.Container
	apiContainer      testcontainers.Container
	importerContainer testcontainers.Container
}

// Start starts the Trustify containers (postgres + api + importer) with an internal network.
func (t *Trustify) Start(ctx context.Context, network string, reuse bool) error {
	logrus.Infof("Starting Trustify containers (reuse=%v)", reuse)

	if err := t.ensureNetwork(ctx); err != nil {
		return fmt.Errorf("failed to create Trustify network: %w", err)
	}

	if err := t.startDatabase(ctx, reuse); err != nil {
		return fmt.Errorf("failed to start Trustify database: %w", err)
	}

	if err := t.startAPI(ctx, network, reuse); err != nil {
		return fmt.Errorf("failed to start Trustify API: %w", err)
	}

	if err := t.startImporter(ctx, reuse); err != nil {
		return fmt.Errorf("failed to start Trustify importer: %w", err)
	}

	if err := t.waitForAPI(ctx); err != nil {
		return fmt.Errorf("trustify API not ready: %w", err)
	}

	logrus.Infof("Trustify started: %s", t.URL)
	return nil
}

func (t *Trustify) ensureNetwork(ctx context.Context) error {
	t.networkName = trustifyNetworkName

	cli := containerRuntimeCLIName()
	cmd := exec.CommandContext(ctx, cli, "network", "inspect", t.networkName)
	if err := cmd.Run(); err == nil {
		logrus.Infof("Trustify network %s already exists", t.networkName)
		return nil
	}

	logrus.Infof("Creating Trustify internal network: %s", t.networkName)
	cmd = exec.CommandContext(ctx, cli, "network", "create", t.networkName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create network %s: %w", t.networkName, err)
	}
	return nil
}

func (t *Trustify) startDatabase(ctx context.Context, reuse bool) error {
	logrus.Info("Starting Trustify postgres container")

	req := testcontainers.ContainerRequest{
		Image:        trustifyDBImage,
		Name:         trustifyDBContainer,
		ExposedPorts: []string{trustifyDBPort},
		Env: map[string]string{
			"POSTGRES_DB":       trustifyDBName,
			"POSTGRES_USER":     trustifyDBUser,
			"POSTGRES_PASSWORD": trustifyDBPassword,
			"PGDATA":            "/var/lib/postgresql/data",
		},
		WaitingFor: wait.ForExec([]string{"pg_isready", "-U", trustifyDBUser}).
			WithStartupTimeout(60 * time.Second),
		SkipReaper: reuse,
	}

	container, err := CreateContainer(ctx, req, reuse, WithNetwork(t.networkName), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start Trustify DB container: %w", err)
	}
	t.dbContainer = container

	logrus.Info("Trustify postgres container started")
	return nil
}

func (t *Trustify) startAPI(ctx context.Context, externalNetwork string, reuse bool) error {
	logrus.Info("Starting Trustify API container")

	req := testcontainers.ContainerRequest{
		Image:        trustifyAPIImage,
		Name:         trustifyAPIContainer,
		ExposedPorts: []string{trustifyAPIPort},
		Env: map[string]string{
			"TRUSTD_DB_HOST":        trustifyDBContainer,
			"TRUSTD_DB_NAME":        trustifyDBName,
			"TRUSTD_DB_USER":        trustifyDBUser,
			"TRUSTD_DB_PASSWORD":    trustifyDBPassword,
			"HTTP_SERVER_BIND_ADDR": "0.0.0.0",
			"AUTH_DISABLED":         "true",
		},
		Cmd: []string{"api", "--devmode"},
		WaitingFor: wait.ForHTTP("/api/v2/sbom").
			WithPort("8080/tcp").
			WithStartupTimeout(2 * time.Minute),
		SkipReaper: reuse,
	}

	container, err := CreateContainer(ctx, req, reuse, WithNetwork(t.networkName), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start Trustify API container: %w", err)
	}
	t.apiContainer = container

	if externalNetwork != "" && externalNetwork != t.networkName {
		cli := containerRuntimeCLIName()
		cmd := exec.CommandContext(ctx, cli, "network", "connect", externalNetwork, trustifyAPIContainer)
		if err := cmd.Run(); err != nil {
			logrus.Warnf("Failed to connect API container to external network %s: %v", externalNetwork, err)
		}
	}

	t.Host = GetHostIP()
	port, err := container.MappedPort(ctx, "8080")
	if err != nil {
		return fmt.Errorf("failed to get Trustify API mapped port: %w", err)
	}
	t.Port = port.Port()
	t.URL = fmt.Sprintf("http://%s", net.JoinHostPort(t.Host, t.Port))
	t.InternalHost = trustifyAPIContainer
	t.InternalPort = "8080"

	logrus.Infof("Trustify API container started: %s", t.URL)
	return nil
}

func (t *Trustify) startImporter(ctx context.Context, reuse bool) error {
	logrus.Info("Starting Trustify importer container")

	req := testcontainers.ContainerRequest{
		Image: trustifyImporterImage,
		Name:  trustifyImporterContainer,
		Env: map[string]string{
			"TRUSTD_DB_HOST":       trustifyDBContainer,
			"TRUSTD_DB_NAME":       trustifyDBName,
			"TRUSTD_DB_USER":       trustifyDBUser,
			"TRUSTD_DB_PASSWORD":   trustifyDBPassword,
			"AUTH_DISABLED":        "true",
			"IMPORTER_WORKING_DIR": "/tmp/trustify-importer",
		},
		Cmd: []string{
			"importer",
			"--concurrency=5",
			"--working-dir=/tmp/trustify-importer",
		},
		SkipReaper: reuse,
	}

	container, err := CreateContainer(ctx, req, reuse, WithNetwork(t.networkName), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start Trustify importer container: %w", err)
	}
	t.importerContainer = container

	logrus.Info("Trustify importer container started")
	return nil
}

func (t *Trustify) waitForAPI(ctx context.Context) error {
	logrus.Info("Waiting for Trustify API to be ready...")

	apiURL := t.URL + "/api/v2/sbom"
	deadline := time.Now().Add(60 * time.Second)
	client := &http.Client{Timeout: 5 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(apiURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				logrus.Info("Trustify API is ready")
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return fmt.Errorf("trustify API not reachable at %s after 60s", apiURL)
}

// InternalURL returns the URL for container-to-container access (e.g., from flightctl-periodic).
func (t *Trustify) InternalURL() string {
	return fmt.Sprintf("http://%s", net.JoinHostPort(t.InternalHost, t.InternalPort))
}

// LoadTestData uploads SBOMs and advisories from the testdata directory to Trustify.
func (t *Trustify) LoadTestData(ctx context.Context) error {
	testDataPath, err := getTrustifyTestDataPath()
	if err != nil {
		return fmt.Errorf("failed to get testdata path: %w", err)
	}

	if err := t.uploadSBOMs(ctx, testDataPath); err != nil {
		return fmt.Errorf("failed to upload SBOMs: %w", err)
	}

	if err := t.uploadAdvisories(ctx, testDataPath); err != nil {
		return fmt.Errorf("failed to upload advisories: %w", err)
	}

	logrus.Info("Trustify test data loaded successfully")
	return nil
}

func (t *Trustify) uploadSBOMs(ctx context.Context, testDataPath string) error {
	sboms := []struct {
		file   string
		digest string
	}{
		{"sbom-digest-a.json", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"sbom-digest-b.json", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{"sbom-digest-c.json", "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	}

	client := &http.Client{Timeout: 30 * time.Second}

	for _, s := range sboms {
		filePath := filepath.Join(testDataPath, s.file)
		if !fileExists(filePath) {
			logrus.Warnf("SBOM file not found, skipping: %s", filePath)
			continue
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read SBOM file %s: %w", s.file, err)
		}

		digestLabel := strings.TrimPrefix(s.digest, "sha256:")
		url := fmt.Sprintf("%s/api/v2/sbom?labels=sha256~%s", t.URL, digestLabel)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to create request for SBOM %s: %w", s.file, err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to upload SBOM %s: %w", s.file, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
			return fmt.Errorf("failed to upload SBOM %s: status=%d, body=%s", s.file, resp.StatusCode, string(body))
		}

		logrus.Infof("Uploaded SBOM %s (digest: %s)", s.file, s.digest[:20]+"...")
	}

	return nil
}

func (t *Trustify) uploadAdvisories(ctx context.Context, testDataPath string) error {
	advisoriesPath := filepath.Join(testDataPath, "advisories")
	if !fileExists(advisoriesPath) {
		logrus.Warn("Advisories directory not found, skipping advisory upload")
		return nil
	}

	advisories := []string{
		"rhsa-2021-4358-glibc.json", // CVE-2021-35942 CRITICAL (9.1), CVE-2021-33574 MEDIUM, CVE-2021-27645 LOW
		"rhsa-2023-5455-glibc.json", // CVE-2023-4911 HIGH (7.8), CVE-2023-4527/4806/4813 MEDIUM
	}

	client := &http.Client{Timeout: 30 * time.Second}

	for _, a := range advisories {
		filePath := filepath.Join(advisoriesPath, a)
		if !fileExists(filePath) {
			logrus.Warnf("Advisory file not found, skipping: %s", filePath)
			continue
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read advisory file %s: %w", a, err)
		}

		url := fmt.Sprintf("%s/api/v2/advisory", t.URL)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to create request for advisory %s: %w", a, err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to upload advisory %s: %w", a, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
			return fmt.Errorf("failed to upload advisory %s: status=%d, body=%s", a, resp.StatusCode, string(body))
		}

		logrus.Infof("Uploaded advisory %s", a)
	}

	return nil
}

func getTrustifyTestDataPath() (string, error) {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return "", err
	}
	p := filepath.Join(projectRoot, "test", "e2e", "vulnerability", "testdata")
	return p, nil
}

// Stop terminates the Trustify containers and removes the internal network.
func (t *Trustify) Stop(ctx context.Context) error {
	var errs []error

	if t.importerContainer != nil {
		if err := t.importerContainer.Terminate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop Trustify importer: %w", err))
		}
	}
	if t.apiContainer != nil {
		if err := t.apiContainer.Terminate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop Trustify API: %w", err))
		}
	}
	if t.dbContainer != nil {
		if err := t.dbContainer.Terminate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop Trustify DB: %w", err))
		}
	}

	if t.networkName != "" {
		cli := containerRuntimeCLIName()
		cmd := exec.CommandContext(ctx, cli, "network", "rm", t.networkName)
		if err := cmd.Run(); err != nil {
			logrus.Warnf("Failed to remove Trustify network %s: %v", t.networkName, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors stopping Trustify: %v", errs)
	}
	return nil
}

// StopTrustifyContainers force-removes Trustify containers and network by name.
// Used by StopServices for cleanup when the Trustify instance is not available.
func StopTrustifyContainers() {
	cli := containerRuntimeCLIName()

	logrus.Infof("Stopping Trustify importer container %s", trustifyImporterContainer)
	if err := podmanRemove(trustifyImporterContainer); err != nil {
		logrus.Warnf("Could not remove %s: %v", trustifyImporterContainer, err)
	}

	logrus.Infof("Stopping Trustify API container %s", trustifyAPIContainer)
	if err := podmanRemove(trustifyAPIContainer); err != nil {
		logrus.Warnf("Could not remove %s: %v", trustifyAPIContainer, err)
	}

	logrus.Infof("Stopping Trustify DB container %s", trustifyDBContainer)
	if err := podmanRemove(trustifyDBContainer); err != nil {
		logrus.Warnf("Could not remove %s: %v", trustifyDBContainer, err)
	}

	logrus.Infof("Removing Trustify network %s", trustifyNetworkName)
	cmd := exec.Command(cli, "network", "rm", trustifyNetworkName)
	if err := cmd.Run(); err != nil {
		logrus.Warnf("Could not remove network %s: %v", trustifyNetworkName, err)
	}
}
