package main

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

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/stretchr/testify/require"
)

func TestWorkerEnrollmentProjection(t *testing.T) {
	for _, defect := range []string{"none", "missing", "partial", "invalid", "source symlink", "parent symlink", "destination symlink", "unsafe destination", "unreadable", "unsupported", "serial", "malformed config", "missing config", "not CA", "expired", "future", "wrong usage"} {
		t.Run("When signing inputs are "+defect+" it should project only safe available enrollment files", func(t *testing.T) {
			pki, runtime := t.TempDir(), t.TempDir()
			require.NoError(t, os.Chmod(runtime, 0700))
			configFile := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(configFile, []byte("{}"), 0600))
			issuer := filepath.Join(pki, "flightctl-api")
			require.NoError(t, os.Mkdir(issuer, 0700))
			cert, key := filepath.Join(issuer, "client-signer.crt"), filepath.Join(issuer, "client-signer.key")
			_, err := crypto.MakeSelfSignedCA(cert, key, "", "projection-fixture", 1)
			require.NoError(t, err)
			public, err := os.ReadFile(cert)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(pki, "ca-bundle.crt"), public, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(issuer, "server.key"), []byte("must-not-project"), 0600))
			require.NoError(t, os.Chmod(key, 0400))
			run := func() (string, error) {
				var output bytes.Buffer
				cmd := NewStandaloneCommand()
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				cmd.SetArgs([]string{"prepare-worker-enrollment", "--pki-dir", pki, "--runtime-dir", runtime, "--config", configFile})
				err := cmd.Execute()
				return output.String(), err
			}
			_, err = run()
			require.NoError(t, err)
			files, err := os.ReadDir(filepath.Join(runtime, "signing"))
			require.NoError(t, err)
			require.Len(t, files, 3)
			info, err := os.Stat(filepath.Join(runtime, "signing/client-signer.key"))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0400), info.Mode().Perm())
			switch defect {
			case "not CA", "expired", "future", "wrong usage":
				pair, err := tls.LoadX509KeyPair(cert, key)
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
				require.NoError(t, os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
			case "malformed config":
				require.NoError(t, os.WriteFile(configFile, []byte("ca: ["), 0600))
			case "missing config":
				require.NoError(t, os.Remove(configFile))
			case "unsupported":
				require.NoError(t, os.WriteFile(configFile, []byte("ca:\n  type: 99"), 0600))
			case "serial":
				require.NoError(t, os.WriteFile(configFile, []byte("ca:\n  internalConfig:\n    serialFile: serial"), 0600))
			case "missing":
				require.NoError(t, os.RemoveAll(issuer))
			case "partial":
				require.NoError(t, os.Remove(key))
			case "invalid":
				require.NoError(t, os.WriteFile(cert, []byte("invalid"), 0600))
			case "unreadable":
				require.NoError(t, os.Chmod(key, 0000))
			case "source symlink":
				require.NoError(t, os.Remove(cert))
				require.NoError(t, os.Symlink(filepath.Join(pki, "ca-bundle.crt"), cert))
			case "parent symlink":
				require.NoError(t, os.Rename(issuer, issuer+"-real"))
				require.NoError(t, os.Symlink(issuer+"-real", issuer))
			case "destination symlink":
				require.NoError(t, os.RemoveAll(filepath.Join(runtime, "signing")))
				require.NoError(t, os.Symlink(issuer, filepath.Join(runtime, "signing")))
			case "unsafe destination":
				require.NoError(t, os.Chmod(runtime, 0777))
			}
			output, err := run()
			if defect == "malformed config" || defect == "missing config" {
				require.Error(t, err)
				files, readErr := os.ReadDir(filepath.Join(runtime, "signing"))
				require.NoError(t, readErr)
				require.Empty(t, files)
				return
			}
			if defect == "destination symlink" || defect == "unsafe destination" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			files, err = os.ReadDir(filepath.Join(runtime, "signing"))
			require.NoError(t, err)
			if defect == "none" {
				require.Len(t, files, 3)
			} else {
				require.Empty(t, files)
				require.Contains(t, output, "enrollment signing unavailable")
			}
			require.NotContains(t, output, "PRIVATE KEY")
		})
	}
}
