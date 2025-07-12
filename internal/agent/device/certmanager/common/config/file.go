package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/common"
)

// FileConfigProvider reads certificate configs from a file
type FileConfigProvider struct {
	filePath string
}

// NewFileConfigProvider creates a new provider that loads configs from a file
func NewFileConfigProvider(filePath string) *FileConfigProvider {
	return &FileConfigProvider{
		filePath: filePath,
	}
}

func (p *FileConfigProvider) Name() string {
	return fmt.Sprintf("file[%s]", p.filePath)
}

// GetCertificateConfigs reads and returns certificate configs from the file
func (p *FileConfigProvider) GetCertificateConfigs() ([]common.CertificateConfig, error) {
	data, err := os.ReadFile(p.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var certs []common.CertificateConfig
	if err := json.Unmarshal(data, &certs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal certificate configs: %w", err)
	}

	return certs, nil
}
