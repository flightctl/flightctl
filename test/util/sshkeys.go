package util

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SSHPrivateKeyPath is the path to a file containing an SSH private key.
// Use this type for parameters that expect a filesystem path (e.g. ssh -i, GIT_SSH_COMMAND).
type SSHPrivateKeyPath string

// SSHPrivateKeyContent is the raw content of an SSH private key (e.g. for API payloads, base64).
// Use this type for parameters that expect the key material, not a path.
type SSHPrivateKeyContent string

// GenerateTempSSHKeyPair creates a temporary directory, generates an ed25519 SSH key pair
// with ssh-keygen, and returns the public key content (one line), the path to the private key,
// and a cleanup function that removes the temp dir. Call cleanup when done (e.g. defer).
func GenerateTempSSHKeyPair() (publicKey string, privateKeyPath SSHPrivateKeyPath, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "flightctl-ssh-keys-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create temp dir for SSH keys: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	privPath := filepath.Join(dir, "id_rsa")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", privPath, "-N", "", "-C", "") // #nosec G204 - test keygen
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		cleanup()
		return "", SSHPrivateKeyPath(""), nil, fmt.Errorf("ssh-keygen failed: %w: %s", runErr, string(out))
	}
	pubBytes, err := os.ReadFile(privPath + ".pub")
	if err != nil {
		cleanup()
		return "", SSHPrivateKeyPath(""), nil, fmt.Errorf("failed to read generated public key: %w", err)
	}
	return strings.TrimSpace(string(pubBytes)), SSHPrivateKeyPath(privPath), cleanup, nil
}
