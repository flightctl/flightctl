package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v2"
)

func RenderTemplates(inputFile string, templatesDir string, outputDir string) error {
	inputData, err := os.ReadFile(inputFile)
	if err != nil {
		return fmt.Errorf("failed to read input file %s: %w", inputFile, err)
	}

	var data interface{}
	if err := yaml.Unmarshal(inputData, &data); err != nil {
		return fmt.Errorf("failed to parse YAML from %s: %w", inputFile, err)
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}

	// Read template files
	templates, err := os.ReadDir(templatesDir)
	if err != nil {
		return fmt.Errorf("failed to read templates directory %s: %w", templatesDir, err)
	}

	// Render each template
	for _, template := range templates {
		if template.IsDir() {
			continue
		}

		templatePath := filepath.Join(templatesDir, template.Name())
		outputPath := filepath.Join(outputDir, template.Name())
		// Strip .template suffix if it exists
		outputPath = strings.TrimSuffix(outputPath, ".template")

		if err := renderTemplate(data, templatePath, outputPath); err != nil {
			return fmt.Errorf("failed to render template %s: %w", template.Name(), err)
		}
	}

	return nil
}

func renderTemplate(data interface{}, templateFile string, outputFile string) error {
	tmpl, err := template.ParseFiles(templateFile)
	if err != nil {
		return fmt.Errorf("failed to parse template file %s: %w", templateFile, err)
	}

	outFile, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file %s: %w", outputFile, err)
	}
	defer outFile.Close()

	if err := tmpl.Execute(outFile, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
}
