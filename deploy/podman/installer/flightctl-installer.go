package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Database  string `yaml:"database"`
	User      string `yaml:"user"`
	Password  string `yaml:"password"`
	AdminUser string `yaml:"admin_user"`
	AdminPass string `yaml:"admin_pass"`
}

func main() {
	var configDir, systemdUnitDir string

	var rootCmd = &cobra.Command{
		Use:   "install",
		Short: "Install the flightctl database container",
		RunE: func(cmd *cobra.Command, args []string) error {
			err := run(configDir, systemdUnitDir)
			if err != nil {
				return err
			}
			return nil
		},
	}

	rootCmd.Flags().StringVarP(&configDir, "config-dir", "c", "deploy/podman", "Configuration directory")
	rootCmd.Flags().StringVarP(&systemdUnitDir, "systemd-unit-dir", "s", "~/.config/containers/systemd", "Writable systemd directory")
	if err := rootCmd.Execute(); err != nil {
		fmt.Println("Error executing command:", err)
		os.Exit(1)
	}
}

func run(configDir string, systemdUnitDir string) error {
	// Read configuration YAML files
	configPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("error reading config file: %v", err)
	}

	// Parse YAML into struct
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("error parsing config file: %v", err)
	}

	// Read template file
	templatePath := filepath.Join(configDir, "flightctl-db/flightctl-db.container.template")
	containerTemplate, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("error reading template file: %v", err)
	}

	// Parse and execute template
	tmpl, err := template.New("container").Parse(string(containerTemplate))
	if err != nil {
		return fmt.Errorf("error parsing template: %v", err)
	}

	var output bytes.Buffer
	if err := tmpl.Execute(&output, config); err != nil {
		return fmt.Errorf("error executing template: %v", err)
	}

	// Write output to file
	outputPath := filepath.Join(systemdUnitDir, "flightctl-db.container")
	if err := os.WriteFile(outputPath, output.Bytes(), 0644); err != nil {
		return fmt.Errorf("error writing output file: %v", err)
	}

	// Move static files
	staticDir := filepath.Join(configDir)
	staticFiles := []string{"flightctl.network", "flightctl.slice"}
	for _, file := range staticFiles {
		src := filepath.Join(staticDir, file)
		dst := filepath.Join(systemdUnitDir, file)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("error copying static file: %v", err)
		}
	}

	fmt.Println("Generated quadlet files successfully.")
	return nil
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destinationFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	_, err = io.Copy(destinationFile, sourceFile)
	if err != nil {
		return err
	}

	return destinationFile.Sync()
}
