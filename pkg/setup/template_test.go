package setup

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

var inputData = map[string]interface{}{
	"name":    "TestApp",
	"version": "1.0.0",
	"config": map[string]interface{}{
		"port": 8080,
		"url":  "https://example.com",
	},
}

const yamlTemplate = `app_name: {{.name}}
app_version: {{.version}}
app_port: {{.config.port}}
app_url: {{.config.url}}
`

const envTemplate = `APP_NAME={{ .name }}
APP_VERSION={{ .version }}
APP_PORT={{ .config.port }}
APP_URL={{ .config.url }}
`

const yamlOutput = `app_name: TestApp
app_version: 1.0.0
app_port: 8080
app_url: https://example.com
`

const envOutput = `APP_NAME=TestApp
APP_VERSION=1.0.0
APP_PORT=8080
APP_URL=https://example.com
`

func TestRenderTemplates(t *testing.T) {
	testCases := []struct {
		name            string
		inputYAML       string
		templates       map[string]string // Map of filename -> template content
		expectedOutputs map[string]string // Map of output filename -> expected content
		expectError     bool
		errorMsg        string
	}{
		{
			name:      "success case with valid templates",
			inputYAML: inputYAML,
			templates: map[string]string{
				"config.yaml.template": yamlTemplate,
				"info.txt":             envTemplate,
			},
			expectedOutputs: map[string]string{
				"config.yaml": yamlOutput,
				"info.txt":    envOutput,
			},
			expectError: false,
		},
		{
			name:        "invalid YAML input",
			inputYAML:   `name: - [invalid`,
			templates:   map[string]string{},
			expectError: true,
			errorMsg:    "failed to parse YAML",
		},
		{
			name:      "invalid template syntax",
			inputYAML: inputYAML,
			templates: map[string]string{
				"bad.template": `{{.name}} {{range .missing}} {{end`,
			},
			expectError: true,
			errorMsg:    "failed to render template",
		},
		{
			name:            "no templates",
			inputYAML:       inputYAML,
			templates:       map[string]string{},
			expectedOutputs: map[string]string{},
			expectError:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var inputFile, templatesDir, outputDir string
			tempDir := t.TempDir()

			// Write input YAML file
			inputFile = filepath.Join(tempDir, "input.yaml")
			err := os.WriteFile(inputFile, []byte(tc.inputYAML), 0600)
			require.NoError(t, err)

			// Create templates directory and write templates
			templatesDir = filepath.Join(tempDir, "templates")
			err = os.MkdirAll(templatesDir, 0755)
			require.NoError(t, err)

			for filename, content := range tc.templates {
				templatePath := filepath.Join(templatesDir, filename)
				err = os.WriteFile(templatePath, []byte(content), 0600)
				require.NoError(t, err)
			}

			outputDir = filepath.Join(tempDir, "output")

			err = RenderTemplates(inputFile, templatesDir, outputDir)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				for filename, expectedContent := range tc.expectedOutputs {
					outputPath := filepath.Join(outputDir, filename)
					assert.FileExists(t, outputPath)

					actualContent, err := os.ReadFile(outputPath)
					require.NoError(t, err)
					assert.Equal(t, expectedContent, string(actualContent))
				}

				entries, err := os.ReadDir(outputDir)
				require.NoError(t, err)
				assert.Len(t, entries, len(tc.expectedOutputs))
			}
		})
	}
}

func TestRenderTemplates_MissingInputFile(t *testing.T) {
	tempDir := t.TempDir()

	inputFile := filepath.Join(tempDir, "nonexistent.yaml")

	templatesDir := filepath.Join(tempDir, "templates")
	err := os.MkdirAll(templatesDir, 0755)
	require.NoError(t, err)

	outputDir := filepath.Join(tempDir, "output")

	err = RenderTemplates(inputFile, templatesDir, outputDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read input file")
}

func TestRenderTemplates_MissingTemplatesDirectory(t *testing.T) {
	tempDir := t.TempDir()

	inputFile := filepath.Join(tempDir, "input.yaml")
	yamlContent := `name: TestApp`
	err := os.WriteFile(inputFile, []byte(yamlContent), 0600)
	require.NoError(t, err)

	templatesDir := filepath.Join(tempDir, "nonexistent")
	outputDir := filepath.Join(tempDir, "output")

	err = RenderTemplates(inputFile, templatesDir, outputDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read templates directory")
}

func TestRenderTemplate(t *testing.T) {
	testCases := []struct {
		name            string
		data            interface{}
		templateContent string
		expectedOutput  string
		expectError     bool
		errorMsg        string
	}{
		{
			name:            "success case with valid template and data",
			data:            inputData,
			templateContent: yamlTemplate,
			expectedOutput:  yamlOutput,
			expectError:     false,
		},
		{
			name:            "template execution error",
			data:            1234, // Invalid data that is not a map/struct
			templateContent: yamlTemplate,
			expectError:     true,
			errorMsg:        "failed to execute template",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var data interface{}
			var templateFile, outputFile string

			tempDir := t.TempDir()

			data = tc.data

			templateFile = filepath.Join(tempDir, "template.txt")
			err := os.WriteFile(templateFile, []byte(tc.templateContent), 0600)
			require.NoError(t, err)

			outputFile = filepath.Join(tempDir, "output.txt")

			err = renderTemplate(data, templateFile, outputFile)

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

	data := map[string]interface{}{"name": "Test"}
	templateFile := filepath.Join(tempDir, "nonexistent.template")
	outputFile := filepath.Join(tempDir, "output.txt")

	err := renderTemplate(data, templateFile, outputFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse template file")
}

func TestRenderTemplate_InvalidOutputPath(t *testing.T) {
	tempDir := t.TempDir()

	data := map[string]interface{}{"name": "Test"}

	templateFile := filepath.Join(tempDir, "template.txt")
	templateContent := `{{.name}}`
	err := os.WriteFile(templateFile, []byte(templateContent), 0600)
	require.NoError(t, err)

	// Create a read-only directory
	readOnlyDir := filepath.Join(tempDir, "readonly")
	err = os.MkdirAll(readOnlyDir, 0444)
	require.NoError(t, err)

	outputFile := filepath.Join(readOnlyDir, "output.txt")

	err = renderTemplate(data, templateFile, outputFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create output file")
}
