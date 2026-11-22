package workerserver

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func workerSigningConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.NewDefault()
	cfg.CA = ca.NewDefault(t.TempDir())
	cfg.CA.InternalConfig.CertFile = filepath.Join(cfg.CA.InternalConfig.CertStore, cfg.CA.InternalConfig.CertFile)
	cfg.CA.InternalConfig.KeyFile = filepath.Join(cfg.CA.InternalConfig.CertStore, cfg.CA.InternalConfig.KeyFile)
	cfg.CA.InternalConfig.CertStore = ""
	_, err := icrypto.MakeSelfSignedCA(cfg.CA.InternalConfig.CertFile, cfg.CA.InternalConfig.KeyFile, "", "worker-test-ca", 1)
	require.NoError(t, err)
	cfg.CA.InternalConfig.CABundleFile = cfg.CA.InternalConfig.CertFile
	return cfg
}

func TestWorkerSigningStartup(t *testing.T) {
	for _, scenario := range []string{"existing", "missing", "invalid", "unreadable", "serial file", "missing CA", "missing internal config", "empty signer", "unsupported backend", "missing bundle", "malformed bundle"} {
		t.Run("When signing material is "+scenario+" it should validate before starting consumers", func(t *testing.T) {
			if scenario == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read files regardless of their permission bits")
			}
			cfg := workerSigningConfig(t)
			certPath := cfg.CA.InternalConfig.CertFile
			original, err := os.ReadFile(certPath)
			require.NoError(t, err)
			switch scenario {
			case "missing CA":
				cfg.CA = nil
			case "missing internal config":
				cfg.CA.InternalConfig = nil
			case "empty signer":
				cfg.CA.DeviceEnrollmentSignerName = ""
			case "unsupported backend":
				cfg.CA.CAType = ca.AsyncInternalCA
			case "missing bundle", "malformed bundle":
				cfg.CA.InternalConfig.CABundleFile = filepath.Join(t.TempDir(), "bundle.pem")
				if scenario == "malformed bundle" {
					require.NoError(t, os.WriteFile(cfg.CA.InternalConfig.CABundleFile, []byte("invalid bundle"), 0600))
				}
			case "missing":
				require.NoError(t, os.Remove(cfg.CA.InternalConfig.KeyFile))
			case "invalid":
				require.NoError(t, os.WriteFile(cfg.CA.InternalConfig.KeyFile, []byte("invalid key"), 0600))
			case "unreadable":
				require.NoError(t, os.Chmod(cfg.CA.InternalConfig.KeyFile, 0000))
			case "serial file":
				cfg.CA.InternalConfig.SerialFile = filepath.Join(t.TempDir(), "serial")
				require.NoError(t, os.WriteFile(cfg.CA.InternalConfig.SerialFile, []byte("02\n"), 0600))
			}
			var diagnostic bytes.Buffer
			log := logrus.New()
			log.SetOutput(&diagnostic)
			server, err := New(cfg, log, nil, nil, nil, nil)
			require.NoError(t, err)
			require.NotNil(t, server)
			if scenario == "existing" {
				require.Empty(t, diagnostic.String())
			} else {
				require.Contains(t, diagnostic.String(), "automatic enrollment disabled")
				if scenario == "serial file" {
					require.Contains(t, diagnostic.String(), "serialFile")
					serial, readErr := os.ReadFile(cfg.CA.InternalConfig.SerialFile)
					require.NoError(t, readErr)
					require.Equal(t, "02\n", string(serial))
				} else {
					if scenario == "missing" {
						_, statErr := os.Stat(cfg.CA.InternalConfig.KeyFile)
						require.ErrorIs(t, statErr, os.ErrNotExist)
					}
				}
			}
			unchanged, err := os.ReadFile(certPath)
			require.NoError(t, err)
			require.Equal(t, original, unchanged)
		})
	}
}

func TestWorkerMissingSigningConfiguration(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {}, {CA: &ca.Config{}}} {
		server, err := New(cfg, logrus.New(), nil, nil, nil, nil)
		require.Nil(t, server)
		require.ErrorContains(t, err, "worker service configuration is required")
	}
}

func TestWorkerUnusableSigningCertificate(t *testing.T) {
	for _, defect := range []string{"not CA", "expired", "future", "wrong usage"} {
		t.Run("When signing certificate is "+defect+" it should disable only enrollment", func(t *testing.T) {
			cfg := workerSigningConfig(t)
			pair, err := tls.LoadX509KeyPair(cfg.CA.InternalConfig.CertFile, cfg.CA.InternalConfig.KeyFile)
			require.NoError(t, err)
			certificate, err := x509.ParseCertificate(pair.Certificate[0])
			require.NoError(t, err)
			switch defect {
			case "not CA":
				certificate.IsCA = false
			case "expired":
				certificate.NotBefore = time.Now().Add(-2 * time.Hour)
				certificate.NotAfter = time.Now().Add(-time.Hour)
			case "future":
				certificate.NotBefore = time.Now().Add(time.Hour)
			case "wrong usage":
				certificate.KeyUsage = x509.KeyUsageDigitalSignature
			}
			der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, certificate.PublicKey, pair.PrivateKey)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfg.CA.InternalConfig.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
			var diagnostic bytes.Buffer
			log := logrus.New()
			log.SetOutput(&diagnostic)
			server, err := New(cfg, log, nil, nil, nil, nil)
			require.NoError(t, err)
			require.NotNil(t, server)
			require.Contains(t, diagnostic.String(), "automatic enrollment disabled")
		})
	}
}
