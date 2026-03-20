package crypto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/stretchr/testify/require"
)

func TestGetServerCABundle(t *testing.T) {
	tempDir := t.TempDir()

	// Create a dummy CA bundle file
	dummyCABundlePath := filepath.Join(tempDir, "dummy-ca-bundle.crt")
	err := os.WriteFile(dummyCABundlePath, []byte("DUMMY CA BUNDLE CONTENT"), 0600)
	require.NoError(t, err)

	cfg := ca.NewDefault(tempDir)
	cfg.ServerCABundleFile = dummyCABundlePath

	// The GetServerCABundle should read and return the dummy CA bundle content
	client := NewCAClient(cfg, nil)

	bundle, err := client.GetServerCABundle()
	require.NoError(t, err)
	require.Equal(t, []byte("DUMMY CA BUNDLE CONTENT"), bundle)
}

func TestGetServerCABundle_Fallback(t *testing.T) {
	tempDir := t.TempDir()

	// If no ServerCABundleFile is provided, it should fall back to GetCABundle()
	cfg := ca.NewDefault(tempDir)

	// Create the fallback CABundleFile
	dummyCABundlePath := CertStorePath(cfg.InternalConfig.CABundleFile, cfg.InternalConfig.CertStore)
	err := os.WriteFile(dummyCABundlePath, []byte("FALLBACK CA BUNDLE CONTENT"), 0600)
	require.NoError(t, err)

	client := NewCAClient(cfg, nil)

	bundle, err := client.GetServerCABundle()
	require.NoError(t, err)
	require.Equal(t, []byte("FALLBACK CA BUNDLE CONTENT"), bundle)
}
