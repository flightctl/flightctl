package infra

import (
	"fmt"
	"path/filepath"

	internalconfig "github.com/flightctl/flightctl/internal/config"
	"sigs.k8s.io/yaml"
)

// EncryptionKeyDir is the in-container directory where encryption key files are mounted.
// Both K8s (via Secret volume) and Quadlet (via bind mount at /etc/flightctl/encryption)
// present keys at this path inside the container.
const EncryptionKeyDir = "/root/.flightctl/encryption"

// ParseEncryptionConfig extracts the encryption block from a service config YAML string
// and returns a typed EncryptionConfig. If no encryption block is present (e.g. Quadlet
// with defaults baked in), it synthesizes the default — activeKeyID=default,
// path=EncryptionKeyDir/key.
func ParseEncryptionConfig(configYAML string) (*internalconfig.EncryptionConfig, error) {
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &root); err != nil {
		return nil, fmt.Errorf("ParseEncryptionConfig: parse config: %w", err)
	}

	if raw, ok := root["encryption"]; ok && raw != nil {
		sub, err := yaml.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("ParseEncryptionConfig: re-marshal encryption block: %w", err)
		}
		var enc internalconfig.EncryptionConfig
		if err := yaml.Unmarshal(sub, &enc); err != nil {
			return nil, fmt.Errorf("ParseEncryptionConfig: parse encryption block: %w", err)
		}
		return &enc, nil
	}

	// No encryption block — synthesize the implicit default.
	return &internalconfig.EncryptionConfig{
		ActiveKeyID: "default",
		Keys: []internalconfig.EncryptionKeyConfig{
			{ID: "default", Path: filepath.Join(EncryptionKeyDir, "key")},
		},
	}, nil
}

// MarshalEncryptionConfigIntoYAML merges the given EncryptionConfig into the provided
// service config YAML string, replacing the existing encryption block (if any).
// Unrelated top-level keys are preserved.
func MarshalEncryptionConfigIntoYAML(configYAML string, enc *internalconfig.EncryptionConfig) (string, error) {
	var root map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &root); err != nil {
		return "", fmt.Errorf("MarshalEncryptionConfigIntoYAML: parse config: %w", err)
	}
	if root == nil {
		root = make(map[string]interface{})
	}

	encBytes, err := yaml.Marshal(enc)
	if err != nil {
		return "", fmt.Errorf("MarshalEncryptionConfigIntoYAML: marshal encryption config: %w", err)
	}
	var encMap interface{}
	if err := yaml.Unmarshal(encBytes, &encMap); err != nil {
		return "", fmt.Errorf("MarshalEncryptionConfigIntoYAML: re-parse encryption config: %w", err)
	}
	root["encryption"] = encMap

	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("MarshalEncryptionConfigIntoYAML: marshal config: %w", err)
	}
	return string(out), nil
}
