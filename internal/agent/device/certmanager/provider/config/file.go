package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

const fileConfigPollInterval = time.Minute

// FileConfigProvider reads certificate configurations from a JSON file and supports
// change detection through file polling. It provides reactive configuration updates
// by monitoring file changes and notifying the certificate manager when changes occur.
type FileConfigProvider struct {
	ctx            context.Context                       // Context for canceling file polling
	filePath       string                                // Path to the configuration file
	lastHash       []byte                                // Hash of last read file content for change detection
	changeNotifier provider.ConfigProviderChangeNotifier // Notifier for configuration changes
}

// NewFileConfigProvider creates a new provider that loads certificate configurations from a JSON file.
// It sets up file polling to detect configuration changes and notify the certificate manager.
func NewFileConfigProvider(ctx context.Context, filePath string) *FileConfigProvider {
	return &FileConfigProvider{
		ctx:      ctx,
		filePath: filePath,
	}
}

// Name returns a unique identifier for this configuration provider that includes the file path.
// This helps distinguish between multiple file providers in logs and management.
func (p *FileConfigProvider) Name() string {
	return fmt.Sprintf("file[%s]", p.filePath)
}

// GetCertificateConfigs reads and returns certificate configurations from the JSON file.
// The file should contain a JSON array of certificate configurations.
func (p *FileConfigProvider) GetCertificateConfigs() ([]provider.CertificateConfig, error) {
	data, err := os.ReadFile(p.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var certs []provider.CertificateConfig
	if err := json.Unmarshal(data, &certs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal certificate configs: %w", err)
	}

	return certs, nil
}

// RegisterConfigChangeChannel registers a channel to receive configuration change notifications.
// It starts file polling to detect changes and triggers notifications when the file content changes.
func (p *FileConfigProvider) RegisterConfigChangeChannel(ch chan<- provider.ConfigProvider, cp provider.ConfigProvider) error {
	sum, err := p.getFileSum256(p.filePath)
	if err != nil {
		return err
	}

	p.lastHash = sum
	if err := p.changeNotifier.RegisterConfigChangeChannel(ch, cp); err != nil {
		return err
	}

	go p.WatchFilePolling(p.ctx, fileConfigPollInterval)
	return nil
}

// WatchFilePolling monitors the configuration file for changes using periodic polling.
// It calculates file hashes and triggers change notifications when the content changes.
// This method runs in a goroutine until the context is canceled.
func (p *FileConfigProvider) WatchFilePolling(ctx context.Context, interval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return

		case <-time.After(interval):
			sum, err := p.getFileSum256(p.filePath)
			if err != nil {
				continue
			}

			if !bytes.Equal(sum, p.lastHash) {
				p.changeNotifier.TriggerConfigChange()
				p.lastHash = sum
			}
		}
	}
}

// getFileSum256 calculates the SHA-256 hash of the file content for change detection.
// This is used to efficiently detect when the file has been modified.
func (p *FileConfigProvider) getFileSum256(filePath string) ([]byte, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	currentHash := sha256.Sum256(data)
	return currentHash[:], nil
}
