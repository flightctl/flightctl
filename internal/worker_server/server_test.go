package workerserver

import (
	"os"
	"path/filepath"
	"testing"

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
	return cfg
}

func TestWorkerSigningStartup(t *testing.T) {
	for _, scenario := range []string{"existing", "missing", "invalid", "unreadable", "serial file"} {
		t.Run("When signing material is "+scenario+" it should validate before starting consumers", func(t *testing.T) {
			if scenario == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read files regardless of their permission bits")
			}
			cfg := workerSigningConfig(t)
			original, err := os.ReadFile(cfg.CA.InternalConfig.CertFile)
			require.NoError(t, err)
			switch scenario {
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
			server, err := New(cfg, logrus.New(), nil, nil, nil, nil)
			if scenario == "existing" {
				require.NoError(t, err)
				require.NotNil(t, server)
			} else {
				require.Error(t, err)
				require.Nil(t, server)
				if scenario == "serial file" {
					require.ErrorContains(t, err, "serialFile")
					serial, readErr := os.ReadFile(cfg.CA.InternalConfig.SerialFile)
					require.NoError(t, readErr)
					require.Equal(t, "02\n", string(serial))
				} else {
					require.ErrorContains(t, err, "loading client-signer certificates")
					if scenario == "missing" {
						_, statErr := os.Stat(cfg.CA.InternalConfig.KeyFile)
						require.ErrorIs(t, statErr, os.ErrNotExist)
					}
				}
			}
			unchanged, err := os.ReadFile(cfg.CA.InternalConfig.CertFile)
			require.NoError(t, err)
			require.Equal(t, original, unchanged)
		})
	}
}

func TestWorkerMissingSigningConfiguration(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {}, {CA: &ca.Config{}}} {
		server, err := New(cfg, logrus.New(), nil, nil, nil, nil)
		require.Nil(t, server)
		require.ErrorContains(t, err, "signing configuration is required")
	}
}
