package auxiliary

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // G505: required for Apache-style htpasswd {SHA} format
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	registryImage         = "quay.io/flightctl/e2eregistry:2"
	registryContainerName = "e2e-registry"
	registryPort          = "5000/tcp"
	registryHostPort      = "5000"
	registriesConfPath    = "/etc/containers/registries.conf.d/flightctl-e2e.conf"

	privateRegistryNginxImage    = "quay.io/flightctl-tests/nginx:1.28-alpine-slim"
	privateRegistryContainerName = "e2e-registry-auth"
	privateRegistryPort          = "5002/tcp"
	privateRegistryHostPort      = "5002"
	defaultAuthUsername          = "testuser"
	defaultAuthPassword          = "testpassword"
)

// AuthenticatedEndpoint holds connection details for the credential-required view of this registry (same backend, nginx + basic auth on another port).
// HostPort is for docker:// / oci:// style refs (host:port), not an OAuth issuer or login URL.
type AuthenticatedEndpoint struct {
	HostPort string
	Port     string
	Username string
	Password string
}

// Registry holds connection info and the container for the aux registry.
type Registry struct {
	URL           string
	Host          string
	Port          string
	Reused        bool // true when the container was already running (reuse=true)
	Authenticated AuthenticatedEndpoint

	container testcontainers.Container
}

// containerExistsByName returns true if a container with the given name exists (running or stopped).
func containerExistsByName(name string) bool {
	cli := containerRuntimeCLIName()
	filter := containerNamePSFilter(cli, name)
	cmd := exec.Command(cli, "ps", "-a", "--filter", filter, "-q")
	out, err := cmd.CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func getContainerLogs(name string) (string, error) {
	cmd := exec.Command("podman", "logs", name)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	cmd = exec.Command("docker", "logs", name)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("container logs failed: %w", err)
	}
	return string(out), nil
}

// Start starts the TLS registry container, then the authenticated (nginx + basic auth) endpoint, and sets URL, Host, Port, Reused, Authenticated.
func (r *Registry) Start(ctx context.Context, network string, reuse bool) error {
	logrus.Infof("Starting registry container (reuse=%v)", reuse)
	r.Reused = reuse && containerExistsByName(registryContainerName)
	certDir, err := ensureRegistryCerts()
	if err != nil {
		return fmt.Errorf("failed to ensure registry certs: %w", err)
	}
	certPath := filepath.Join(certDir, "registry.crt")
	keyPath := filepath.Join(certDir, "registry.key")
	req := testcontainers.ContainerRequest{
		Image:        registryImage,
		Name:         registryContainerName,
		ExposedPorts: []string{registryHostPort + ":" + registryPort},
		Env: map[string]string{
			"REGISTRY_HTTP_TLS_CERTIFICATE": "/certs/registry.crt",
			"REGISTRY_HTTP_TLS_KEY":         "/certs/registry.key",
		},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: certPath, ContainerFilePath: "/certs/registry.crt", FileMode: 0644},
			{HostFilePath: keyPath, ContainerFilePath: "/certs/registry.key", FileMode: 0600},
		},
		WaitingFor: wait.ForHTTP("/v2/").WithPort("5000").WithTLS(true).WithAllowInsecure(true),
		SkipReaper: reuse,
	}
	container, err := CreateContainer(ctx, req, reuse, WithNetwork(network), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start registry container: %w", err)
	}
	r.container = container
	hostIP := GetHostIP()
	r.Port = registryHostPort
	// For IPv6: use localhost for host-side podman and container name for container-to-container DNS.
	// IPv6 addresses in image names are parsed as transport prefixes (e.g., "fd2e:" looks like "docker:")
	if strings.Contains(hostIP, ":") {
		r.URL = fmt.Sprintf("localhost:%s", r.Port)
		r.Host = registryContainerName
		logrus.Infof("IPv6 detected (%s) - using localhost:%s for host, %s for containers",
			hostIP, r.Port, r.Host)
	} else {
		r.Host = hostIP
		r.URL = fmt.Sprintf("%s:%s", r.Host, r.Port)
	}
	if err := configureInsecureRegistry(r.URL); err != nil {
		logrus.Warnf("Failed to configure insecure registry: %v", err)
	}
	logrus.Infof("Registry container started: %s (TLS enabled)", r.URL)

	if err := r.startAuthenticatedEndpoint(ctx, certDir, network, reuse); err != nil {
		return fmt.Errorf("failed to start authenticated registry endpoint: %w", err)
	}
	return nil
}

func (r *Registry) startAuthenticatedEndpoint(ctx context.Context, certDir, network string, reuse bool) error {
	logrus.Info("Starting authenticated registry endpoint (nginx + basic auth)")
	logrus.Infof("Authenticated endpoint upstream: %s:%s (network: %s)", r.Host, r.Port, network)

	certPath := filepath.Join(certDir, "registry.crt")
	keyPath := filepath.Join(certDir, "registry.key")

	htpasswdContent := generateHtpasswd(defaultAuthUsername, defaultAuthPassword)
	nginxConf := generateNginxConf(r.Host, r.Port)
	logrus.Debugf("Nginx config:\n%s", nginxConf)

	tmpDir, err := os.MkdirTemp("", "e2e-registry-auth-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for auth files: %w", err)
	}
	htpasswdPath := filepath.Join(tmpDir, "htpasswd")
	if err := os.WriteFile(htpasswdPath, []byte(htpasswdContent), 0600); err != nil {
		return fmt.Errorf("failed to write htpasswd: %w", err)
	}
	nginxConfPath := filepath.Join(tmpDir, "nginx.conf")
	if err := os.WriteFile(nginxConfPath, []byte(nginxConf), 0600); err != nil {
		return fmt.Errorf("failed to write nginx.conf: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        privateRegistryNginxImage,
		Name:         privateRegistryContainerName,
		ExposedPorts: []string{privateRegistryHostPort + ":" + privateRegistryPort},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: certPath, ContainerFilePath: "/certs/registry.crt", FileMode: 0644},
			{HostFilePath: keyPath, ContainerFilePath: "/certs/registry.key", FileMode: 0600},
			{HostFilePath: htpasswdPath, ContainerFilePath: "/auth/htpasswd", FileMode: 0644},
			{HostFilePath: nginxConfPath, ContainerFilePath: "/etc/nginx/nginx.conf", FileMode: 0644},
		},
		WaitingFor: wait.ForHTTP("/v2/").WithPort(privateRegistryHostPort).WithTLS(true).WithAllowInsecure(true).WithBasicAuth(defaultAuthUsername, defaultAuthPassword),
		SkipReaper: reuse,
	}

	_, err = CreateContainer(ctx, req, reuse, WithNetwork(network), WithHostAccess())
	if err != nil {
		logrus.Errorf("Authenticated registry endpoint container failed to start. Attempting to fetch logs...")
		if logs, logErr := getContainerLogs(privateRegistryContainerName); logErr == nil {
			logrus.Errorf("Authenticated registry endpoint container logs:\n%s", logs)
		} else {
			logrus.Errorf("Could not fetch container logs: %v", logErr)
		}
		return fmt.Errorf("failed to start authenticated registry endpoint container: %w", err)
	}

	r.Authenticated = AuthenticatedEndpoint{
		HostPort: net.JoinHostPort(r.Host, privateRegistryHostPort),
		Port:     privateRegistryHostPort,
		Username: defaultAuthUsername,
		Password: defaultAuthPassword,
	}

	logrus.Infof("Authenticated registry endpoint started: %s", r.Authenticated.HostPort)
	return nil
}

func generateNginxConf(registryHost, registryPort string) string {
	upstreamAddr := net.JoinHostPort(registryHost, registryPort)
	return fmt.Sprintf(`error_log /dev/stderr warn;

events {
  worker_connections 1024;
}

http {
  upstream registry {
    server %s;
  }

  server {
    listen 5002 ssl;
    listen [::]:5002 ssl;
    ssl_certificate /certs/registry.crt;
    ssl_certificate_key /certs/registry.key;

    client_max_body_size 0;
    chunked_transfer_encoding on;

    location / {
      auth_basic "Private Registry";
      auth_basic_user_file /auth/htpasswd;
      proxy_pass https://registry;
      proxy_ssl_verify off;
      proxy_set_header Host $host;
      proxy_set_header X-Real-IP $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto $scheme;
    }
  }
}
`, upstreamAddr)
}

// generateHtpasswd creates an Apache-style htpasswd entry using SHA1.
func generateHtpasswd(username, password string) string {
	hash := sha1.Sum([]byte(password)) //nolint:gosec
	encoded := base64.StdEncoding.EncodeToString(hash[:])
	return fmt.Sprintf("%s:{SHA}%s\n", username, encoded)
}

func ensureRegistryCerts() (string, error) {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return "", err
	}
	certDir := filepath.Join(projectRoot, "bin", "e2e-certs", "pki", "CA")
	caKeyPath := filepath.Join(certDir, "ca.key")
	caCertPath := filepath.Join(certDir, "ca.crt")
	registryKeyPath := filepath.Join(certDir, "registry.key")
	if !fileExists(caKeyPath) || !fileExists(caCertPath) {
		return "", fmt.Errorf("CA certificates not found at %s - run 'make prepare-e2e-test' first", certDir)
	}
	// Registry leaf cert only; CA is created by prepare-e2e-test and injected into device images.
	logrus.Info("Generating registry (leaf) certificate from existing CA for current host IP...")
	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read CA key: %w", err)
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		return "", fmt.Errorf("failed to decode CA key PEM")
	}
	var caKey *rsa.PrivateKey
	caKey, err = x509.ParsePKCS1PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		key, err2 := x509.ParsePKCS8PrivateKey(caKeyBlock.Bytes)
		if err2 != nil {
			return "", fmt.Errorf("failed to parse CA key: %w", err2)
		}
		var ok bool
		caKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("CA key is not RSA")
		}
	}
	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return "", err
	}
	caCertBlock, _ := pem.Decode(caCertPEM)
	if caCertBlock == nil {
		return "", fmt.Errorf("failed to decode CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(caCertBlock.Bytes)
	if err != nil {
		return "", err
	}
	registryKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	hostIP := GetHostIP()
	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: "e2e-registry"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "e2e-registry"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP(hostIP)},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &registryKey.PublicKey, caKey)
	if err != nil {
		return "", err
	}
	keyFile, err := os.OpenFile(registryKeyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	_ = pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(registryKey)})
	keyFile.Close()
	registryCertPath := filepath.Join(certDir, "registry.crt")
	certFile, err := os.OpenFile(registryCertPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return "", err
	}
	_ = pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()
	logrus.Infof("Generated registry certificate in %s (IP: %s)", certDir, hostIP)
	return certDir, nil
}

func configureInsecureRegistry(registryURL string) error {
	if existingConfig, err := os.ReadFile(registriesConfPath); err == nil && string(existingConfig) != "" {
		return nil
	}
	config := fmt.Sprintf(`[[registry]]
location = "%s"
insecure = true
`, registryURL)
	cmd := exec.Command("sudo", "tee", registriesConfPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, _ = stdin.Write([]byte(config))
	stdin.Close()
	return cmd.Wait()
}

// GetRegistrySSHPrivateKeyPath returns the path to the pre-generated SSH private key in bin/.ssh.
// For registry use only. Created by create_e2e_certs.sh.
func (s *Services) GetRegistrySSHPrivateKeyPath() (util.SSHPrivateKeyPath, error) {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return "", err
	}
	p := filepath.Join(projectRoot, "bin", ".ssh", "id_rsa")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("registry SSH private key not found at %s: %w (run create_e2e_certs.sh)", p, err)
	}
	return util.SSHPrivateKeyPath(p), nil
}

// GetRegistrySSHPublicKeyPath returns the path to the pre-generated SSH public key in bin/.ssh.
// For registry use only. Created by create_e2e_certs.sh.
func (s *Services) GetRegistrySSHPublicKeyPath() (string, error) {
	projectRoot, err := getProjectRoot()
	if err != nil {
		return "", err
	}
	p := filepath.Join(projectRoot, "bin", ".ssh", "id_rsa.pub")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("registry SSH public key not found at %s: %w (run create_e2e_certs.sh)", p, err)
	}
	return p, nil
}
