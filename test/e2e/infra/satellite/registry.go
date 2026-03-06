package satellite

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
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
	registryImage         = "registry:2"
	registryContainerName = "e2e-registry"
	registryPort          = "5000/tcp"
	registryHostPort      = "5000"
	registriesConfPath    = "/etc/containers/registries.conf.d/flightctl-e2e.conf"
)

// containerExistsByName returns true if a container with the given name exists (running or stopped).
func containerExistsByName(name string) bool {
	//nolint:gosec // G204: name is only ever the constant registryContainerName
	cmd := exec.Command("podman", "ps", "-a", "--filter", "name=^"+name+"$", "-q")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func (s *Services) startRegistry(ctx context.Context) error {
	logrus.Infof("Starting registry container (reuse=%v)", s.reuse)
	s.registryReused = s.reuse && containerExistsByName(registryContainerName)
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
		// When reusing, skip Ryuk so the reaper does not mark the container for removal when this process exits (next suite reuses it).
		SkipReaper: s.reuse,
	}
	container, err := CreateContainer(ctx, req, s.reuse, WithNetwork(s.network), WithHostAccess())
	if err != nil {
		return fmt.Errorf("failed to start registry container: %w", err)
	}
	s.registry = container
	s.RegistryHost = GetHostIP()
	s.RegistryPort = registryHostPort
	s.RegistryURL = fmt.Sprintf("%s:%s", s.RegistryHost, s.RegistryPort)
	if err := configureInsecureRegistry(s.RegistryURL); err != nil {
		logrus.Warnf("Failed to configure insecure registry: %v", err)
	}
	logrus.Infof("Registry container started: %s (TLS enabled)", s.RegistryURL)
	return nil
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
