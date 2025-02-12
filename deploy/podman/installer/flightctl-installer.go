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
	Global *globalConfig `yaml:"global,omitempty"`
	DB     *dbConfig     `yaml:"db,omitempty"`
	KV     *kvConfig     `yaml:"kv,omitempty"`
	API    *apiConfig    `yaml:"api,omitempty"`

	// Set by flags
	ConfigDir      string
	UserConfigDir  string
	SystemdUnitDir string
}

type globalConfig struct {
	Ports      *portsConfig `yaml:"ports"`
	BaseDomain string       `yaml:"baseDomain"`
}

type imageConfig struct {
	Image string `yaml:"image"`
}

type portsConfig struct {
	DB    int `yaml:"db"`
	KV    int `yaml:"kv"`
	API   int `yaml:"api"`
	Agent int `yaml:"agent"`
	GRPC  int `yaml:"grpc"`
	UI    int `yaml:"ui"`
}

type dbConfig struct {
	Image         *imageConfig `yaml:"image"`
	Name          string       `yaml:"name"`
	AdminUser     string       `yaml:"adminUser"`
	AdminPassword string       `yaml:"adminPassword"`
	User          string       `yaml:"user"`
	Password      string       `yaml:"password"`
}

type kvConfig struct {
	Image    *imageConfig `yaml:"image"`
	Password string       `yaml:"password"`
	Save     string       `yaml:"save"`
}

type apiConfig struct {
	Image *imageConfig `yaml:"image"`
}

var services = []string{"flightctl-db", "flightctl-kv", "flightctl-api"}

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
	config.SystemdUnitDir = systemdUnitDir

	// Handle each service
	for _, service := range services {
		if err := ensureServiceFiles(service, config); err != nil {
			return fmt.Errorf("error handling service %s: %v", service, err)
		}
	}

	// Move top level files like the .network and .slice files
	err = ensureFiles(config.ConfigDir, config.SystemdUnitDir, config)
	if err != nil {
		return fmt.Errorf("error moving top level static files: %v", err)
	}

	fmt.Println("Generated quadlet files successfully.")
	return nil
}

func ensureServiceFiles(serviceName string, config Config) error {
	serviceBasePath := filepath.Join(config.ConfigDir, serviceName)
	serviceConfigPath := filepath.Join(serviceBasePath, fmt.Sprintf("%s-config", serviceName))

	// Write systemd unit files
	err := ensureFiles(serviceBasePath, config.SystemdUnitDir, config)
	if err != nil {
		return fmt.Errorf("error writing systemd unit files for %s: %v", serviceName, err)
	}

	// Write config files if they exist
	if _, err := os.Stat(serviceConfigPath); !os.IsNotExist(err) {
		err = ensureFiles(serviceConfigPath, config.UserConfigDir, config)
		if err != nil {
			return fmt.Errorf("error writing config files for %s: %v", serviceName, err)
		}
	}

	return nil
}

func ensureFiles(sourceDir string, writePath string, config Config) error {
	files, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("error reading source directory: %v", err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		fileName := file.Name()
		if filepath.Ext(fileName) == ".template" {
			if err := writeTemplate(fileName, sourceDir, writePath, config); err != nil {
				return err
			}
		} else {
			dst := filepath.Join(writePath, fileName)
			if err := ensurePath(writePath); err != nil {
				return err
			}

			src := filepath.Join(sourceDir, fileName)
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("error copying static file: %v", err)
			}
		}
	}

	return nil
}

func writeTemplate(fileName string, sourceDir string, writePath string, config Config) error {
	templateFilePath := filepath.Join(sourceDir, fileName)
	templateContent, err := os.ReadFile(templateFilePath)
	if err != nil {
		return fmt.Errorf("error reading template file %s: %v", fileName, err)
	}

	tmpl, err := template.New(fileName).Parse(string(templateContent))
	if err != nil {
		return fmt.Errorf("error parsing template %s: %v", fileName, err)
	}

	var output bytes.Buffer
	if err := tmpl.Execute(&output, config); err != nil {
		return fmt.Errorf("error executing template %s: %v", fileName, err)
	}

	if err := ensurePath(writePath); err != nil {
		return err
	}
	outputFilePath := filepath.Join(writePath, fileName)
	outputFilePath = outputFilePath[:len(outputFilePath)-len(".template")] // Remove .template extension

	if err := os.WriteFile(outputFilePath, output.Bytes(), 0644); err != nil {
		return fmt.Errorf("error writing output file %s: %v", outputFilePath, err)
	}

	return nil
}

func ensurePath(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, os.ModePerm); err != nil {
			return fmt.Errorf("error creating path %s: %v", path, err)
		}
	}
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
