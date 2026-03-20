package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const inputYAML = `global:
  baseDomain: example.com
  auth:
    type: none
db:
  external: disabled
service:
  rateLimit:
    enabled: false
observability: {}
`

const yamlTemplate = `baseDomain: {{.global.baseDomain}}
authType: {{.global.auth.type}}
`

const yamlOutput = `baseDomain: example.com
authType: none
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
			name:            "invalid YAML",
			inputContent:    `this is not valid yaml: [[[`,
			templateContent: `{{.global.baseDomain}}`,
			expectError:     true,
			errorMsg:        "failed to parse",
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
	err := os.WriteFile(inputFile, []byte(inputYAML), 0600)
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
	err := os.WriteFile(inputFile, []byte(inputYAML), 0600)
	require.NoError(t, err)

	templateFile := filepath.Join(tempDir, "template.txt")
	templateContent := `{{.global.baseDomain}}`
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
	assert.Equal(t, "example.com", string(actualContent))
}

func TestRenderWithData_WithFuncMap(t *testing.T) {
	testCases := []struct {
		name            string
		templateContent string
		expectedOutput  string
	}{
		{
			name:            "uses replace function from FuncMap",
			templateContent: `{{ replace "." "-" .global.baseDomain }}`,
			expectedOutput:  "example-com",
		},
		{
			name:            "uses upper function from FuncMap",
			templateContent: `{{ upper .global.baseDomain }}`,
			expectedOutput:  "EXAMPLE.COM",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			tempDir := t.TempDir()

			inputFile := filepath.Join(tempDir, "input.yaml")
			require.NoError(os.WriteFile(inputFile, []byte(inputYAML), 0600))

			templateFile := filepath.Join(tempDir, "template.txt")
			require.NoError(os.WriteFile(templateFile, []byte(tc.templateContent), 0600))

			outputFile := filepath.Join(tempDir, "output.txt")

			funcMap := template.FuncMap{
				"replace": func(old, new string, input any) string {
					return strings.ReplaceAll(input.(string), old, new)
				},
				"upper": func(s any) string {
					return strings.ToUpper(s.(string))
				},
			}

			err := Render(inputFile, templateFile, outputFile, WithFuncMap(funcMap))
			require.NoError(err)

			actual, err := os.ReadFile(outputFile)
			require.NoError(err)
			assert.Equal(t, tc.expectedOutput, string(actual))
		})
	}
}

func TestRenderWithData_WithoutFuncMap(t *testing.T) {
	require := require.New(t)
	tempDir := t.TempDir()

	templateFile := filepath.Join(tempDir, "template.txt")
	require.NoError(os.WriteFile(templateFile, []byte(`{{ replace "." "-" .global.baseDomain }}`), 0600))

	outputFile := filepath.Join(tempDir, "output.txt")

	err := RenderWithData(map[string]interface{}{
		"global": map[string]interface{}{"baseDomain": "example.com"},
	}, templateFile, outputFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse template file")
}

func TestRenderTemplate_InvalidOutputPath(t *testing.T) {
	tempDir := t.TempDir()

	inputFile := filepath.Join(tempDir, "input.yaml")
	err := os.WriteFile(inputFile, []byte(inputYAML), 0600)
	require.NoError(t, err)

	templateFile := filepath.Join(tempDir, "template.txt")
	templateContent := `{{.global.baseDomain}}`
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
