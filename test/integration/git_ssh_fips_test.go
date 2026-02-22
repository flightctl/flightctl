package integration_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	b64 "encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/gliderlabs/ssh"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// startSshGitServerForFIPSTest starts a minimal SSH server for testing git clone operations
// Returns the port number and a cleanup function that should be called to stop the server
func startSshGitServerForFIPSTest(t *testing.T, pubKey gossh.PublicKey) (string, func()) {
	t.Helper()

	// Use port 0 to let the OS assign a free ephemeral port
	// This avoids port conflicts in parallel CI
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// Extract the actual port that was assigned
	port := fmt.Sprintf("%d", ln.Addr().(*net.TCPAddr).Port)

	// Create SSH server with handler
	server := &ssh.Server{
		Handler: func(s ssh.Session) {
			// Simulate a minimal git server response
			// In a real scenario, this would be a proper git-upload-pack interaction
			fmt.Fprintf(s, "# service=git-upload-pack\n")
			fmt.Fprintf(s, "0000")
			<-s.Context().Done()
		},
		PublicKeyHandler: func(ctx ssh.Context, key ssh.PublicKey) bool {
			return ssh.KeysEqual(key, pubKey)
		},
	}

	// Start server in background
	go func() {
		if err := server.Serve(ln); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			log.Printf("SSH server error: %v", err)
		}
	}()

	// Give the server a moment to start accepting connections
	time.Sleep(100 * time.Millisecond)

	// Return port and cleanup function
	return port, func() {
		server.Close()
		ln.Close()
	}
}

// TestGitSSHCloneWithFIPS tests that SSH git clone operations work correctly in FIPS mode
// This test reproduces the bug where go-git defaults to non-FIPS-compliant algorithms like curve25519
func TestGitSSHCloneWithFIPS(t *testing.T) {
	// Check if OPENSSL_FORCE_FIPS_MODE is set to simulate FIPS environment
	fipsMode := os.Getenv("OPENSSL_FORCE_FIPS_MODE") == "1"
	if !fipsMode {
		t.Skip("Skipping FIPS test - set OPENSSL_FORCE_FIPS_MODE=1 to run")
	}

	require := require.New(t)

	// Generate RSA key pair for SSH authentication
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	require.NoError(err)

	publicRsaKey, err := gossh.NewPublicKey(&privateKey.PublicKey)
	require.NoError(err)

	// Start SSH git server with OS-assigned ephemeral port
	testPort, cleanup := startSshGitServerForFIPSTest(t, publicRsaKey)
	defer cleanup()

	// Encode private key to PEM format
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   x509.MarshalPKCS1PrivateKey(privateKey),
	})

	privKey := b64.StdEncoding.EncodeToString(privatePEM)

	// Create Repository spec with SSH configuration
	spec := api.RepositorySpec{}
	err = spec.FromGitRepoSpec(api.GitRepoSpec{
		Url:  fmt.Sprintf("ssh://root@127.0.0.1:%s/test.git", testPort),
		Type: api.GitRepoSpecTypeGit,
		SshConfig: &api.SshConfig{
			SshPrivateKey:          &privKey,
			SkipServerVerification: lo.ToPtr(true), // Skip host key verification for test
		},
	})
	require.NoError(err)

	repo := &domain.Repository{
		Metadata: api.ObjectMeta{Name: lo.ToPtr("test-fips-ssh-repo")},
		Spec:     spec,
	}

	// Attempt to clone the repository
	// In FIPS mode WITH the fix, this should succeed by using FIPS-compliant algorithms
	_, _, err = tasks.CloneGitRepo(repo, nil, nil, nil)

	// With the fix implemented, SSH clone should succeed in FIPS mode
	// The fix applies FIPS-compliant algorithms (ecdh-sha2-nistp*, diffie-hellman-group*-sha256)
	// instead of non-compliant ones (curve25519)
	if err != nil {
		// The clone may fail due to the minimal SSH server, but it must NOT fail
		// due to FIPS algorithm restrictions
		errMsg := err.Error()
		require.NotContains(errMsg, "curve25519", "FIPS algorithm restriction should not cause failure")
		require.NotContains(errMsg, "curve25519-sha256", "FIPS algorithm restriction should not cause failure")
		require.NotContains(errMsg, "no common algorithm", "SSH algorithm negotiation should succeed in FIPS mode")
		require.NotContains(errMsg, "ssh-ed25519", "Ed25519 should not be required in FIPS mode")
		t.Logf("Clone error (unrelated to FIPS crypto): %v", err)
	} else {
		t.Logf("SSH clone succeeded in FIPS mode with FIPS-compliant algorithms")
	}
}

// TestGitSSHCloneNonFIPS tests that SSH git clone works in non-FIPS mode
// This ensures backward compatibility - existing functionality should continue to work
func TestGitSSHCloneNonFIPS(t *testing.T) {
	// Only run this when NOT in FIPS mode
	fipsMode := os.Getenv("OPENSSL_FORCE_FIPS_MODE") == "1"
	if fipsMode {
		t.Skip("Skipping non-FIPS test - OPENSSL_FORCE_FIPS_MODE is set")
	}

	require := require.New(t)

	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	require.NoError(err)

	publicRsaKey, err := gossh.NewPublicKey(&privateKey.PublicKey)
	require.NoError(err)

	// Start SSH server with OS-assigned ephemeral port
	testPort, cleanup := startSshGitServerForFIPSTest(t, publicRsaKey)
	defer cleanup()

	// Encode private key
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   x509.MarshalPKCS1PrivateKey(privateKey),
	})

	privKey := b64.StdEncoding.EncodeToString(privatePEM)

	// Create Repository spec
	spec := api.RepositorySpec{}
	err = spec.FromGitRepoSpec(api.GitRepoSpec{
		Url:  fmt.Sprintf("ssh://root@127.0.0.1:%s/test.git", testPort),
		Type: api.GitRepoSpecTypeGit,
		SshConfig: &api.SshConfig{
			SshPrivateKey:          &privKey,
			SkipServerVerification: lo.ToPtr(true),
		},
	})
	require.NoError(err)

	repo := &domain.Repository{
		Metadata: api.ObjectMeta{Name: lo.ToPtr("test-non-fips-ssh-repo")},
		Spec:     spec,
	}

	// In non-FIPS mode, this should work with default algorithms
	_, _, err = tasks.CloneGitRepo(repo, nil, nil, nil)

	// Note: This might still fail because we're using a minimal SSH server
	// The important part is that it should NOT fail due to FIPS restrictions
	if err != nil {
		// Ensure the error is not related to algorithm negotiation failures
		errMsg := err.Error()
		require.NotContains(errMsg, "no common algorithm", "SSH algorithm negotiation should succeed in non-FIPS mode")
		t.Logf("Clone error (may be expected with minimal server): %v", err)
	}
}
