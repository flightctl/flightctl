package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTemplate(t *testing.T) {
	tests := []struct {
		name         string
		templateData templateContext
		inputTemplate string
		expectedContent string
		expectError  bool
	}{
		{
			name: "valid template",
			templateData: templateContext{
				Name:        "test-chart",
				Description: "A test chart",
			},
			inputTemplate: "name: {{.Name}}\ndescription: {{.Description}}",
			expectedContent: "name: test-chart\ndescription: A test chart",
			expectError:   false,
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

func TestApplyFlavorChartOverride(t *testing.T) {
	tests := []struct {
		name           string
		profileKey     string
		distro         string
		relver         string
		setupFiles     func(tmpDir string) string
		expectedError  string
		expectOverride bool
	}{
		{
			name:       "community flavor without Chart.yaml (should succeed)",
			profileKey: "community",
			distro:     "community",
			relver:     "el9",
			setupFiles: func(tmpDir string) string {
				// Create base chart but no flavor chart
				chartContent := "name: test-chart\nannotations:\n  existing: value"
				chartPath := filepath.Join(tmpDir, "Chart.yaml")
				os.WriteFile(chartPath, []byte(chartContent), 0644)
				return chartPath
			},
			expectOverride: false,
		},
		{
			name:       "redhat flavor without Chart.yaml (should fail)",
			profileKey: "redhat",
			distro:     "redhat",
			relver:     "el9",
			setupFiles: func(tmpDir string) string {
				chartContent := "name: test-chart"
				chartPath := filepath.Join(tmpDir, "Chart.yaml")
				os.WriteFile(chartPath, []byte(chartContent), 0644)
				return chartPath
			},
			expectedError: "Red Hat flavor Chart.yaml missing",
		},
		{
			name:       "redhat flavor with Chart.yaml (should succeed)",
			profileKey: "redhat",
			distro:     "redhat",
			relver:     "el9",
			setupFiles: func(tmpDir string) string {
				// Create base chart
				chartContent := "name: test-chart\nannotations:\n  existing: value"
				chartPath := filepath.Join(tmpDir, "Chart.yaml")
				os.WriteFile(chartPath, []byte(chartContent), 0644)

				// Create flavor override chart
				flavorDir := filepath.Join(tmpDir, "packaging", "flavors", "redhat-el9")
				os.MkdirAll(flavorDir, 0755)
				flavorContent := "annotations:\n  redhat.edition: \"true\""
				os.WriteFile(filepath.Join(flavorDir, "Chart.yaml"), []byte(flavorContent), 0644)

				return chartPath
			},
			expectOverride: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Setup test files
			chartPath := tt.setupFiles(tmpDir)

			// Set environment variables
			originalDistro := os.Getenv("DISTRO")
			originalRelver := os.Getenv("RELVER")
			defer func() {
				os.Setenv("DISTRO", originalDistro)
				os.Setenv("RELVER", originalRelver)
			}()

			os.Setenv("DISTRO", tt.distro)
			os.Setenv("RELVER", tt.relver)

			// Change working directory to match expected relative paths
			originalWd, _ := os.Getwd()
			defer os.Chdir(originalWd)

			// Create the expected directory structure for relative paths
			helmDir := filepath.Join(tmpDir, "deploy", "helm")
			os.MkdirAll(helmDir, 0755)
			os.Chdir(helmDir)

			// Copy chart to expected location
			localChartPath := "flightctl/Chart.yaml"
			os.MkdirAll("flightctl", 0755)
			chartBytes, _ := os.ReadFile(chartPath)
			os.WriteFile(localChartPath, chartBytes, 0644)

			// Override the global chartOutPath for this test
			originalChartOutPath := chartOutPath
			chartOutPath = localChartPath
			defer func() { chartOutPath = originalChartOutPath }()

			// Run the function
			err := applyFlavorChartOverride(tt.profileKey)

			// Check error expectation
			if tt.expectedError != "" {
				if err == nil {
					t.Errorf("Expected error containing '%s' but got none", tt.expectedError)
					return
				}
				if !strings.Contains(err.Error(), tt.expectedError) {
					t.Errorf("Expected error containing '%s', got: %v", tt.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// For successful cases, verify the result
			if tt.expectOverride {
				// Check that the chart was modified with flavor overrides
				resultBytes, err := os.ReadFile(localChartPath)
				if err != nil {
					t.Fatalf("Failed to read result chart: %v", err)
				}

				resultContent := string(resultBytes)
				if !strings.Contains(resultContent, "redhat.edition") {
					t.Errorf("Expected flavor annotation 'redhat.edition' to be merged into chart")
				}
				if !strings.Contains(resultContent, "existing: value") {
					t.Errorf("Expected original annotation 'existing: value' to be preserved")
				}
			}
		})
	}
}

func TestDeepMergeMaps(t *testing.T) {
	tests := []struct {
		name     string
		dst      map[string]interface{}
		src      map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name: "merge nested maps",
			dst: map[string]interface{}{
				"annotations": map[string]interface{}{
					"existing": "value",
					"keep":     "me",
				},
				"name": "original",
			},
			src: map[string]interface{}{
				"annotations": map[string]interface{}{
					"new": "annotation",
				},
				"version": "1.0.0",
			},
			expected: map[string]interface{}{
				"annotations": map[string]interface{}{
					"existing": "value",
					"keep":     "me",
					"new":      "annotation",
				},
				"name":    "original",
				"version": "1.0.0",
			},
		},
		{
			name: "overwrite non-map values",
			dst: map[string]interface{}{
				"name":    "old",
				"version": "0.1.0",
			},
			src: map[string]interface{}{
				"name": "new",
			},
			expected: map[string]interface{}{
				"name":    "new",
				"version": "0.1.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy of dst to avoid modifying the test case
			dst := make(map[string]interface{})
			for k, v := range tt.dst {
				if mapVal, ok := v.(map[string]interface{}); ok {
					dstMap := make(map[string]interface{})
					for mk, mv := range mapVal {
						dstMap[mk] = mv
					}
					dst[k] = dstMap
				} else {
					dst[k] = v
				}
			}

			deepMergeMaps(dst, tt.src)

			// Compare the result
			if !equalMaps(dst, tt.expected) {
				t.Errorf("Expected %+v, got %+v", tt.expected, dst)
			}
		})
	}
}

// Helper function to compare maps deeply
func equalMaps(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		bv, exists := b[k]
		if !exists {
			return false
		}

		if mapA, okA := v.(map[string]interface{}); okA {
			if mapB, okB := bv.(map[string]interface{}); okB {
				if !equalMaps(mapA, mapB) {
					return false
				}
			} else {
				return false
			}
		} else if v != bv {
			return false
		}
	}

	return true
}