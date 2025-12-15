package renderer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/sirupsen/logrus"
)

type ActionType int

const (
	ActionCopyFile ActionType = iota
	ActionCopyDir
	ActionCreateEmptyFile
	ActionCreateEmptyDir
)

const (
	RegularFileMode    os.FileMode = 0644 // Regular files
	ExecutableFileMode os.FileMode = 0755 // Executable files and directories
)

type InstallAction struct {
	Action      ActionType
	Source      string
	Destination string
	Template    bool
	Mode        os.FileMode
}

type ImageConfig struct {
	Image string `mapstructure:"image"`
	Tag   string `mapstructure:"tag"`
}

type RendererConfig struct {
	// Output directories
	ReadOnlyConfigOutputDir  string `mapstructure:"readonly-config-dir"`
	WriteableConfigOutputDir string `mapstructure:"writeable-config-dir"`
	QuadletFilesOutputDir    string `mapstructure:"quadlet-dir"`
	SystemdUnitOutputDir     string `mapstructure:"systemd-dir"`
	BinOutputDir             string `mapstructure:"bin-dir"`

	FlightctlServicesTagOverride string `mapstructure:"flightctl-services-tag-override"`
	FlightctlUiTagOverride       bool   `mapstructure:"flightctl-ui-tag-override"`

	// Images
	Api               ImageConfig `mapstructure:"api"`
	Periodic          ImageConfig `mapstructure:"periodic"`
	Worker            ImageConfig `mapstructure:"worker"`
	AlertExporter     ImageConfig `mapstructure:"alert-exporter"`
	CliArtifacts      ImageConfig `mapstructure:"cli-artifacts"`
	AlertmanagerProxy ImageConfig `mapstructure:"alertmanager-proxy"`
	PamIssuer         ImageConfig `mapstructure:"pam-issuer"`
	Ui                ImageConfig `mapstructure:"ui"`
	DbSetup           ImageConfig `mapstructure:"db-setup"`
	Db                ImageConfig `mapstructure:"db"`
	Kv                ImageConfig `mapstructure:"kv"`
	Alertmanager      ImageConfig `mapstructure:"alertmanager"`
}

func NewRendererConfig() *RendererConfig {
	return &RendererConfig{
		ReadOnlyConfigOutputDir:  "/usr/share/flightctl",
		WriteableConfigOutputDir: "/etc/flightctl",
		QuadletFilesOutputDir:    "/usr/share/containers/systemd",
		SystemdUnitOutputDir:     "/usr/lib/systemd/system",
		BinOutputDir:             "/usr/bin",
	}
}

func processInstallManifest(manifest []InstallAction, config *RendererConfig, log logrus.FieldLogger) error {
	for _, action := range manifest {
		switch action.Action {
		case ActionCopyFile:
			if err := processFile(action.Source, action.Destination, action.Template, action.Mode, config); err != nil {
				return fmt.Errorf("failed to process file %s: %w", action.Source, err)
			}
			log.Infof("Processed file: %s -> %s (template=%t)", action.Source, action.Destination, action.Template)

		case ActionCopyDir:
			if err := copyDir(action.Source, action.Destination, action.Mode); err != nil {
				return fmt.Errorf("failed to copy directory %s to %s: %w", action.Source, action.Destination, err)
			}
			log.Infof("Copied directory: %s -> %s", action.Source, action.Destination)

		case ActionCreateEmptyFile:
			if err := createEmptyFile(action.Destination, action.Mode, log); err != nil {
				return fmt.Errorf("failed to create empty file %s: %w", action.Destination, err)
			}
			log.Infof("Created empty file: %s", action.Destination)

		case ActionCreateEmptyDir:
			if err := createEmptyDirectory(action.Destination, action.Mode, log); err != nil {
				return fmt.Errorf("failed to create empty directory %s: %w", action.Destination, err)
			}
			log.Infof("Created empty directory: %s", action.Destination)

		default:
			return fmt.Errorf("unknown action type: %v", action.Action)
		}
	}
	return nil
}

func processFile(sourcePath, destPath string, isTemplate bool, mode os.FileMode, config *RendererConfig) error {
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, ExecutableFileMode); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read source file: %w", err)
	}

	var finalContent []byte
	if isTemplate {
		tmpl, err := template.New(filepath.Base(sourcePath)).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template: %w", err)
		}

		var buf strings.Builder
		if err := tmpl.Execute(&buf, config); err != nil {
			return fmt.Errorf("failed to execute template: %w", err)
		}
		finalContent = []byte(buf.String())
	} else {
		finalContent = content
	}

	if err := os.WriteFile(destPath, finalContent, mode); err != nil {
		return fmt.Errorf("failed to write destination file: %w", err)
	}

	return nil
}

func createEmptyFile(destPath string, mode os.FileMode, log logrus.FieldLogger) error {
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, ExecutableFileMode); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	if _, err := os.Stat(destPath); err == nil {
		log.Infof("File already exists, skipping: %s", destPath)
		return nil
	}

	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	file.Close()

	if err := os.Chmod(destPath, mode); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	return nil
}

func createEmptyDirectory(destPath string, mode os.FileMode, log logrus.FieldLogger) error {
	if stat, err := os.Stat(destPath); err == nil {
		if stat.IsDir() {
			log.Infof("Directory already exists, skipping: %s", destPath)
			return nil
		}
		return fmt.Errorf("path exists but is not a directory: %s", destPath)
	}

	if err := os.MkdirAll(destPath, mode); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	return nil
}

func copyDir(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(dst, ExecutableFileMode); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath, mode); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath, mode); err != nil {
				return err
			}
		}
	}

	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	if err := os.Chmod(dst, mode); err != nil {
		return err
	}

	return nil
}

func (config *RendererConfig) ApplyFlightctlServicesTagOverride(log logrus.FieldLogger) {
	if config.FlightctlServicesTagOverride == "" {
		return
	}

	tag := config.FlightctlServicesTagOverride
	log.Infof("Applying flightctl services tag override: %s", tag)

	config.Api.Tag = tag
	config.Periodic.Tag = tag
	config.Worker.Tag = tag
	config.AlertExporter.Tag = tag
	config.CliArtifacts.Tag = tag
	config.AlertmanagerProxy.Tag = tag
	config.PamIssuer.Tag = tag
	config.DbSetup.Tag = tag

	if config.FlightctlUiTagOverride {
		// For release builds, UI tag must be overridden
		log.Infof("Applying tag override to UI service: %s", tag)
		config.Ui.Tag = tag
	} else {
		// For development builds, UI tag is kept as defined in images.yaml
		log.Infof("Skipping UI tag override (keeping value from images.yaml: %s)", config.Ui.Tag)
	}
}

// RenderQuadlets orchestrates all installation operations
func RenderQuadlets(config *RendererConfig, log logrus.FieldLogger) error {
	log.Info("Starting installation")

	config.ApplyFlightctlServicesTagOverride(log)

	manifest := servicesManifest(config)
	if err := processInstallManifest(manifest, config, log); err != nil {
		return fmt.Errorf("failed to process install manifest: %w", err)
	}

	log.Info("Installation complete")
	return nil
}
