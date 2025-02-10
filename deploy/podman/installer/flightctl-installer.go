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

	// Set by flags
	ConfigDir     string `yaml:"config_dir"`
	UserConfigDir string `yaml:"user_config_dir"`
}

func main() {
	var configDir, systemdUnitDir, userConfigDir string

	var rootCmd = &cobra.Command{
		Use:   "install",
		Short: "Install the flightctl database container",
		RunE: func(cmd *cobra.Command, args []string) error {
			err := run(configDir, systemdUnitDir, userConfigDir)
			if err != nil {
				return err
			}
			return nil
		},
	}

	rootCmd.Flags().StringVarP(&configDir, "config-dir", "c", "/etc/flightctl", "Configuration directory")
	rootCmd.Flags().StringVarP(&systemdUnitDir, "systemd-unit-dir", "s", "~/.config/containers/systemd", "Writable systemd directory")
	rootCmd.Flags().StringVarP(&userConfigDir, "user-config-dir", "u", "~/.config/flightctl", "Writable config directory")
	if err := rootCmd.Execute(); err != nil {
		fmt.Println("Error executing command:", err)
		os.Exit(1)
	}
}

func run(configDir string, systemdUnitDir string, userConfigDir string) error {
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

	// Set config dir
	config.ConfigDir = configDir
	config.UserConfigDir = userConfigDir

	// Read template file
	services := []string{"flightctl-db", "flightctl-kv"}
	for _, service := range services {
		templatePath := filepath.Join(configDir, fmt.Sprintf("%s/%s.container.template", service, service))
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
		outputPath := filepath.Join(systemdUnitDir, fmt.Sprintf("%s.container", service))
		if err := os.WriteFile(outputPath, output.Bytes(), 0644); err != nil {
			return fmt.Errorf("error writing output file: %v", err)
		}
	}

	// Move static files
	staticFiles := []string{"flightctl.network", "flightctl.slice"}
	for _, file := range staticFiles {
		src := filepath.Join(configDir, file)
		dst := filepath.Join(systemdUnitDir, file)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("error copying static file: %v", err)
		}
	}

	// Move config files
	mountConfigFiles := []string{"redis.conf"}
	for _, file := range mountConfigFiles {
		src := filepath.Join(configDir, file)
		dst := filepath.Join(userConfigDir, file)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("error copying config file: %v", err)
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
