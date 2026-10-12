package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	packagedMappingsPath = "../../packaging/flightctl/label-sync/mappings.yaml"
	valuesTemplatePath   = "flightctl/values.yaml.gotmpl"
	beginMarker          = "    # BEGIN GENERATED INITIAL LABEL SYNC MAPPINGS"
	endMarker            = "    # END GENERATED INITIAL LABEL SYNC MAPPINGS"
)

func main() {
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate() error {
	mappings, err := os.ReadFile(packagedMappingsPath)
	if err != nil {
		return fmt.Errorf("read packaged mapping defaults %s: %w", packagedMappingsPath, err)
	}
	var resources []map[string]any
	if err := yaml.Unmarshal(mappings, &resources); err != nil {
		return fmt.Errorf("parse packaged mapping defaults %s: %w", packagedMappingsPath, err)
	}
	if len(resources) == 0 {
		return fmt.Errorf("packaged mapping defaults %s are empty", packagedMappingsPath)
	}

	template, err := os.ReadFile(valuesTemplatePath)
	if err != nil {
		return fmt.Errorf("read Helm values template %s: %w", valuesTemplatePath, err)
	}
	generated, err := replaceGeneratedMappings(string(template), string(mappings))
	if err != nil {
		return err
	}
	if generated == string(template) {
		return nil
	}
	if err := os.WriteFile(valuesTemplatePath, []byte(generated), 0o644); err != nil {
		return fmt.Errorf("write Helm values template %s: %w", valuesTemplatePath, err)
	}
	fmt.Printf("Generated default Helm mappings in %s from %s\n", valuesTemplatePath, packagedMappingsPath)
	return nil
}

func replaceGeneratedMappings(template, mappings string) (string, error) {
	if strings.Count(template, beginMarker) != 1 || strings.Count(template, endMarker) != 1 {
		return "", fmt.Errorf("Helm values template must contain exactly one generated mappings block")
	}

	begin := strings.Index(template, beginMarker)
	end := strings.Index(template, endMarker)
	if begin >= end {
		return "", fmt.Errorf("generated mappings block markers are out of order")
	}

	contentStart := begin + len(beginMarker)
	if contentStart >= len(template) || template[contentStart] != '\n' || template[end-1] != '\n' {
		return "", fmt.Errorf("generated mappings block markers must be on separate lines")
	}

	indentedMappings := indentYAML(strings.TrimRight(mappings, "\r\n"), 4)
	return template[:contentStart+1] + indentedMappings + template[end-1:], nil
}

func indentYAML(yamlText string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(yamlText, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}
