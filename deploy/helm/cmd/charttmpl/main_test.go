package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTemplate(t *testing.T) {
	tests := []struct {
		name            string
		templateData    templateContext
		inputTemplate   string
		expectedContent string
		expectError     bool
	}{
		{
			name: "valid template",
			templateData: templateContext{
				Name:        "test-chart",
				Description: "A test chart",
			},
			inputTemplate:   "name: {{.Name}}\ndescription: {{.Description}}",
			expectedContent: "name: test-chart\ndescription: A test chart",
			expectError:     false,
		},
		{
			name: "template with missing key should error",
			templateData: templateContext{
				Name: "test-chart",
			},
			inputTemplate: "name: {{.Name}}\ndescription: {{.MissingKey}}",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary input file
			tmpDir := t.TempDir()
			inputPath := filepath.Join(tmpDir, "input.tmpl")
			outputPath := filepath.Join(tmpDir, "output.yaml")

			if err := os.WriteFile(inputPath, []byte(tt.inputTemplate), 0644); err != nil {
				t.Fatalf("Failed to create test template: %v", err)
			}

			// Run the function
			err := runTemplate(inputPath, outputPath, tt.templateData)

			// Check error expectation
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// Check output content
			outputBytes, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("Failed to read output file: %v", err)
			}

			outputContent := strings.TrimSpace(string(outputBytes))
			if outputContent != tt.expectedContent {
				t.Errorf("Expected content:\n%s\nGot:\n%s", tt.expectedContent, outputContent)
			}
		})
	}
}

func TestRunTemplateFileErrors(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(tmpDir string) (inputPath, outputPath string)
		expectError string
	}{
		{
			name: "input file does not exist",
			setupFunc: func(tmpDir string) (string, string) {
				return filepath.Join(tmpDir, "nonexistent.tmpl"), filepath.Join(tmpDir, "output.yaml")
			},
			expectError: "reading template",
		},
		{
			name: "output directory does not exist",
			setupFunc: func(tmpDir string) (string, string) {
				inputPath := filepath.Join(tmpDir, "input.tmpl")
				os.WriteFile(inputPath, []byte("name: test"), 0644)
				return inputPath, filepath.Join(tmpDir, "nonexistent", "output.yaml")
			},
			expectError: "creating output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			inputPath, outputPath := tt.setupFunc(tmpDir)

			err := runTemplate(inputPath, outputPath, templateContext{})

			if err == nil {
				t.Errorf("Expected error containing '%s' but got none", tt.expectError)
				return
			}

			if !strings.Contains(err.Error(), tt.expectError) {
				t.Errorf("Expected error containing '%s', got: %v", tt.expectError, err)
			}
		})
	}
}
