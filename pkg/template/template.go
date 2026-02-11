package template

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"sigs.k8s.io/yaml"
)

func Render(inputFile string, templateFile string, outputFile string) error {
	inputData, err := os.ReadFile(inputFile)
	if err != nil {
		return fmt.Errorf("failed to read input file %s: %w", inputFile, err)
	}

	var data interface{}
	if err := yaml.Unmarshal(inputData, &data); err != nil {
		return fmt.Errorf("failed to parse YAML from %s: %w", inputFile, err)
	}

	return RenderWithData(data, templateFile, outputFile)
}

func RenderWithData(data interface{}, templateFile string, outputFile string) error {
	tmpl, err := template.ParseFiles(templateFile)
	if err != nil {
		return fmt.Errorf("failed to parse template file %s: %w", templateFile, err)
	}

	// Ensure output directory exists
	outputDir := filepath.Dir(outputFile)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
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
