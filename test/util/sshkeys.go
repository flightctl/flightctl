package util

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/flightctl/flightctl/test/e2e/infra/satellite"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/sirupsen/logrus"
)

type sshKeyCacheT struct {
	path string
	once sync.Once
	err  error
}

var (
	sshKeyCache       sshKeyCacheT
	sshPublicKeyCache sshKeyCacheT
)

func initSSHPublicKeyPath() (string, error) {
	if p := setup.GetDefaultProviders(); p != nil && p.Secrets != nil {
		data, err := p.Secrets.GetSecretData(context.Background(), E2E_NAMESPACE, "e2e-git-ssh-keys", "id_rsa.pub")
		if err == nil && len(data) > 0 {
			logrus.Info("Using SSH public key from Kubernetes Secret (infra)")
			tempFile, err := os.CreateTemp("", "e2e-git-ssh-key-pub-*")
			if err != nil {
				return "", fmt.Errorf("failed to create temp file for SSH public key: %w", err)
			}
			if err := os.WriteFile(tempFile.Name(), data, 0600); err != nil {
				return "", fmt.Errorf("failed to write SSH public key to temp file: %w", err)
			}
			tempFile.Close()
			return tempFile.Name(), nil
		}
	}
	keyPath := filepath.Join("..", "..", "..", "bin", ".ssh", "id_rsa.pub")
	absPath, err := filepath.Abs(keyPath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for SSH public key: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", fmt.Errorf("SSH public key not found at %s: %w", absPath, err)
	}
	logrus.Info("Using SSH public key from local file")
	return absPath, nil
}

func initSSHPrivateKeyPath() (string, error) {
	if p := setup.GetDefaultProviders(); p != nil && p.Secrets != nil {
		data, err := p.Secrets.GetSecretData(context.Background(), E2E_NAMESPACE, "e2e-git-ssh-keys", "id_rsa")
		if err == nil && len(data) > 0 {
			logrus.Info("Using SSH private key from Kubernetes Secret (infra)")
			tempFile, err := os.CreateTemp("", "e2e-git-ssh-key-*")
			if err != nil {
				return "", fmt.Errorf("failed to create temp file for SSH key: %w", err)
			}
			if err := os.WriteFile(tempFile.Name(), data, 0600); err != nil {
				return "", fmt.Errorf("failed to write SSH key to temp file: %w", err)
			}
			tempFile.Close()
			return tempFile.Name(), nil
		}
	}
	path, err := satellite.GetSSHPrivateKeyPath()
	if err == nil {
		logrus.Info("Using SSH private key from infra/local file")
		return path, nil
	}
	return "", err
}

// GetSSHPublicKeyPath returns the path to the SSH public key file (e2e-git-ssh-keys Secret, then local bin/.ssh).
func GetSSHPublicKeyPath() (string, error) {
	sshPublicKeyCache.once.Do(func() {
		sshPublicKeyCache.path, sshPublicKeyCache.err = initSSHPublicKeyPath()
	})
	return sshPublicKeyCache.path, sshPublicKeyCache.err
}

// GetSSHPrivateKeyPath returns the path to the SSH private key file (e2e-git-ssh-keys Secret, then satellite/local).
func GetSSHPrivateKeyPath() (string, error) {
	sshKeyCache.once.Do(func() {
		sshKeyCache.path, sshKeyCache.err = initSSHPrivateKeyPath()
	})
	return sshKeyCache.path, sshKeyCache.err
}

// GetSSHPrivateKey returns the SSH private key content.
func GetSSHPrivateKey() (string, error) {
	keyPath, err := GetSSHPrivateKeyPath()
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read SSH private key from %s: %w", keyPath, err)
	}
	return string(content), nil
}
