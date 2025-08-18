package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CreateUniqueYAMLFile creates a temporary YAML file with unique resource names based on test ID
func CreateUniqueYAMLFile(originalYAMLPath, testID string) (string, error) {
	// Read the original YAML file
	yamlContent, err := os.ReadFile(GetTestExamplesYamlPath(originalYAMLPath))
	if err != nil {
		return "", fmt.Errorf("failed to read YAML file %s: %w", originalYAMLPath, err)
	}

	// Replace placeholder with test ID
	content := string(yamlContent)
	content = strings.ReplaceAll(content, "{{test_id}}", testID)

	// Create a temporary file with unique name
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("%s-%s.yaml", strings.TrimSuffix(originalYAMLPath, ".yaml"), testID))

	err = os.WriteFile(tempFile, []byte(content), 0600)
	if err != nil {
		return "", fmt.Errorf("failed to write temporary YAML file: %w", err)
	}

	return tempFile, nil
}

// CleanupTempYAMLFile removes the temporary YAML file
func CleanupTempYAMLFile(tempFilePath string) {
	if tempFilePath != "" {
		os.Remove(tempFilePath)
	}
}

// CleanupTempYAMLFiles removes multiple temporary YAML files
func CleanupTempYAMLFiles(tempFilePaths []string) {
	for _, tempFile := range tempFilePaths {
		CleanupTempYAMLFile(tempFile)
	}
}
