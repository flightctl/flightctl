package tpm_test

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/stretchr/testify/require"
)

func TestConfiguredTPMTrustPaths(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "root.pem")
	backend, err := crypto.MakeSelfSignedCA(certPath, filepath.Join(dir, "root.key"), "", "trust-fixture", 1)
	require.NoError(t, err)
	root := backend.GetCABundleX509()[0]
	badPath := filepath.Join(dir, "invalid.pem")
	require.NoError(t, os.WriteFile(badPath, []byte("not a certificate"), 0600))
	missingPath := filepath.Join(dir, "absent.pem")
	for _, scenario := range []struct {
		name    string
		paths   []string
		valid   bool
		invalid bool
	}{
		{"unconfigured", nil, false, false},
		{"missing map or key", []string{missingPath}, false, true},
		{"invalid roots", []string{badPath}, false, true},
		{"configured anchor", []string{certPath}, true, false},
		{"partially available roots", []string{missingPath, badPath, certPath}, true, false},
	} {
		t.Run("When trust has "+scenario.name+" it should use only available valid anchors", func(t *testing.T) {
			pool, err := tpm.LoadCAsFromPaths(scenario.paths)
			if scenario.invalid {
				require.Error(t, err)
				require.Nil(t, pool)
				return
			}
			require.NoError(t, err)
			if !scenario.valid {
				require.Nil(t, pool)
				return
			}
			_, err = root.Verify(x509.VerifyOptions{Roots: pool})
			require.NoError(t, err)
			require.Error(t, tpm.VerifyTCGCSRChainOfTrustWithRoots([]byte("unverified report"), pool))
		})
	}
}
