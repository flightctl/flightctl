package auxiliary

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	keycloakImage          = "quay.io/keycloak/keycloak:26.5.5"
	keycloakContainerName  = "e2e-keycloak"
	keycloakPort           = "8080/tcp"
	keycloakManagementPort = "9000/tcp" // health endpoints (Keycloak 25+)
	keycloakRealmName      = "flightctl"
	// KeycloakE2EClientSecret is the client secret for flightctl-client in the e2e realm.
	KeycloakE2EClientSecret = "e2e-flightctl-client-secret" //nolint:gosec // G101: e2e test client secret only
)

// Keycloak holds connection info and the container for the aux Keycloak.
type Keycloak struct {
	URL       string
	Host      string
	Port      string
	container testcontainers.Container
}

// Start starts the Keycloak container and sets URL, Host, Port.
func (k *Keycloak) Start(ctx context.Context, network string, reuse bool) error {
	logrus.Info("Starting Keycloak container (reuse=true)")
	realmPath, err := getKeycloakRealmPath()
	if err != nil {
		return fmt.Errorf("failed to get Keycloak realm path: %w", err)
	}
	// Health must be enabled at build time (see https://www.keycloak.org/observability/health).
	// Use custom entrypoint: build with --health-enabled=true, then start-dev with realm import.
	// Health endpoints are on port 9000 in Keycloak 25+.
	req := testcontainers.ContainerRequest{
		Image:        keycloakImage,
		Name:         keycloakContainerName,
		ExposedPorts: []string{keycloakPort, keycloakManagementPort},
		Entrypoint:   []string{"/bin/bash", "-c"},
		Cmd:          []string{"/opt/keycloak/bin/kc.sh build --health-enabled=true && /opt/keycloak/bin/kc.sh start-dev --import-realm"},
		Env: map[string]string{
			"KC_BOOTSTRAP_ADMIN_USERNAME": "admin",
			"KC_BOOTSTRAP_ADMIN_PASSWORD": "admin",
			"KC_HEALTH_ENABLED":           "true",
		},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: realmPath, ContainerFilePath: "/opt/keycloak/data/import/flightctl-realm.json", FileMode: 0644},
		},
		WaitingFor: wait.ForAll(
			wait.ForHTTP("/health/ready").
				WithPort("9000/tcp").
				WithAllowInsecure(true).
				WithStartupTimeout(2*time.Minute),
			wait.ForHTTP("/realms/"+keycloakRealmName+"/.well-known/openid-configuration").
				WithPort("8080/tcp").
				WithAllowInsecure(true).
				WithStartupTimeout(30*time.Second),
		),
		SkipReaper: reuse,
	}
	container, err := CreateContainer(ctx, req, reuse, WithNetwork(network), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start Keycloak container: %w", err)
	}
	k.container = container
	k.Host = GetHostIP()
	port, err := container.MappedPort(ctx, "8080")
	if err != nil {
		return fmt.Errorf("failed to get Keycloak mapped port: %w", err)
	}
	k.Port = port.Port()
	k.URL = fmt.Sprintf("http://%s", net.JoinHostPort(k.Host, k.Port))
	logrus.Infof("Keycloak container started: %s (realm: %s)", k.URL, keycloakRealmName)

	// Wait until the realm is reachable from the host (same path the CLI will use).
	// Handles reuse (no wait ran) and port-mapping delay.
	discoveryURL := k.IssuerURL() + "/.well-known/openid-configuration"
	deadline := time.Now().Add(30 * time.Second)
	client := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(discoveryURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("keycloak realm not reachable at %s after 30s", discoveryURL)
}

func getKeycloakRealmPath() (string, error) {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return "", err
	}
	p := filepath.Join(projectRoot, "test", "e2e", "infra", "auxiliary", "keycloak", "flightctl-realm.json")
	if !fileExists(p) {
		return "", fmt.Errorf("Keycloak realm file not found at %s", p)
	}
	return p, nil
}

// IssuerURL returns the OIDC issuer URL for the flightctl realm (e.g. http://host:port/realms/flightctl).
func (k *Keycloak) IssuerURL() string {
	return k.URL + "/realms/" + keycloakRealmName
}
