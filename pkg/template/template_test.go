package template

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inputYAML = `name: TestApp
version: 1.0.0
config:
  port: 8080
  url: https://example.com
`

const yamlTemplate = `app_name: {{.name}}
app_version: {{.version}}
app_port: {{.config.port}}
app_url: {{.config.url}}
`

const yamlOutput = `app_name: TestApp
app_version: 1.0.0
app_port: 8080
app_url: https://example.com
`

func TestRenderTemplate(t *testing.T) {
	testCases := []struct {
		name            string
		inputContent    string
		templateContent string
		expectedOutput  string
		expectError     bool
		errorMsg        string
	}{
		{
			name:            "renders template with input data",
			inputContent:    inputYAML,
			templateContent: yamlTemplate,
			expectedOutput:  yamlOutput,
			expectError:     false,
		},
		{
			name:            "template execution error",
			inputContent:    "1234", // Invalid YAML that will parse but fail template execution
			templateContent: yamlTemplate,
			expectError:     true,
			errorMsg:        "failed to execute template",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()

			inputFile := filepath.Join(tempDir, "input.yaml")
			err := os.WriteFile(inputFile, []byte(tc.inputContent), 0600)
			require.NoError(t, err)

			templateFile := filepath.Join(tempDir, "template.txt")
			err = os.WriteFile(templateFile, []byte(tc.templateContent), 0600)
			require.NoError(t, err)

			outputFile := filepath.Join(tempDir, "output.txt")

			err = Render(inputFile, templateFile, outputFile)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				actualContent, err := os.ReadFile(outputFile)
				require.NoError(t, err)
				assert.Equal(t, tc.expectedOutput, string(actualContent))
			}
		})
	}
}

func TestRenderTemplate_InvalidTemplateFile(t *testing.T) {
	tempDir := t.TempDir()

	inputFile := filepath.Join(tempDir, "input.yaml")
	err := os.WriteFile(inputFile, []byte("name: Test"), 0600)
	require.NoError(t, err)

	templateFile := filepath.Join(tempDir, "nonexistent.template")
	outputFile := filepath.Join(tempDir, "output.txt")

	err = Render(inputFile, templateFile, outputFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse template file")
}

func TestRenderTemplate_CreatesParentDirectories(t *testing.T) {
	tempDir := t.TempDir()

	inputFile := filepath.Join(tempDir, "input.yaml")
	err := os.WriteFile(inputFile, []byte("name: Test"), 0600)
	require.NoError(t, err)

	templateFile := filepath.Join(tempDir, "template.txt")
	templateContent := `{{.name}}`
	err = os.WriteFile(templateFile, []byte(templateContent), 0600)
	require.NoError(t, err)

	// Output file in nested directories that don't exist yet
	outputFile := filepath.Join(tempDir, "output", "nested", "deep", "file.txt")

	err = Render(inputFile, templateFile, outputFile)
	assert.NoError(t, err)

	// Verify the file was created and directories exist
	assert.FileExists(t, outputFile)
	actualContent, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	assert.Equal(t, "Test", string(actualContent))
}

func TestRenderTemplate_InvalidOutputPath(t *testing.T) {
	tempDir := t.TempDir()

	inputFile := filepath.Join(tempDir, "input.yaml")
	err := os.WriteFile(inputFile, []byte("name: Test"), 0600)
	require.NoError(t, err)

	templateFile := filepath.Join(tempDir, "template.txt")
	templateContent := `{{.name}}`
	err = os.WriteFile(templateFile, []byte(templateContent), 0600)
	require.NoError(t, err)

	// Create a read-only directory
	readOnlyDir := filepath.Join(tempDir, "readonly")
	err = os.MkdirAll(readOnlyDir, 0444)
	require.NoError(t, err)

	outputFile := filepath.Join(readOnlyDir, "output.txt")

	err = Render(inputFile, templateFile, outputFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create output file")
}
