package aap_rbac_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/sirupsen/logrus"
)

// clientConfigSnapshot stores the client config file contents for restoration.
type clientConfigSnapshot struct {
	path    string
	exists  bool
	content []byte
}

// captureClientConfigSnapshot records the current client config file contents.
func captureClientConfigSnapshot() (*clientConfigSnapshot, error) {
	configPath, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return nil, fmt.Errorf("resolve default client config path: %w", err)
	}

	snapshot := &clientConfigSnapshot{path: configPath}
	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return snapshot, nil
		}
		return nil, fmt.Errorf("read client config %q: %w", configPath, err)
	}

	snapshot.exists = true
	snapshot.content = append([]byte(nil), content...)
	return snapshot, nil
}

// restoreClientConfigSnapshot restores the client config file and refreshes the harness client.
func restoreClientConfigSnapshot(harness *e2e.Harness, snapshot *clientConfigSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("client config snapshot is nil")
	}
	if strings.TrimSpace(snapshot.path) == "" {
		return fmt.Errorf("client config snapshot path is empty")
	}

	if snapshot.exists {
		if err := os.MkdirAll(filepath.Dir(snapshot.path), 0o755); err != nil {
			return fmt.Errorf("create client config directory for %q: %w", snapshot.path, err)
		}
		if err := os.WriteFile(snapshot.path, snapshot.content, 0o600); err != nil {
			return fmt.Errorf("restore client config %q: %w", snapshot.path, err)
		}
		logrus.Infof("Restored client config at %s", snapshot.path)
		if harness != nil {
			if err := harness.RefreshClient(); err != nil {
				return fmt.Errorf("refresh client after restoring config %q: %w", snapshot.path, err)
			}
		}
		return nil
	}

	if err := os.Remove(snapshot.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove generated client config %q: %w", snapshot.path, err)
	}
	logrus.Infof("Removed generated client config at %s", snapshot.path)
	return nil
}
