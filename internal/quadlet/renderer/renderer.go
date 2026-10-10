package renderer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/sirupsen/logrus"
)

type ActionType int

const (
	ActionCopyFile ActionType = iota
	ActionCopyDir
	ActionCopyBinary
	ActionCreateEmptyFile
	ActionCreateEmptyDir
	ActionWriteFile
	ActionCreateSymlink
)

const (
	RegularFileMode    os.FileMode = 0644 // Regular files
	ExecutableFileMode os.FileMode = 0755 // Executable files and directories
	ShadowFileMode     os.FileMode = 0600 // Shadow files (password hashes) - root only
)

var (
	DefaultServiceConfigPath = "/etc/flightctl/service-config.yaml"
	DefaultAAPClientIDPath   = "/etc/flightctl/pki/aap-client-id"
	DefaultAuthCACertPath    = "/etc/flightctl/pki/auth/ca.crt"
)

// getQuadletTemplateFuncMap returns the template function map for quadlet rendering.
// This includes the public fleet template functions plus internal-only functions.
func getQuadletTemplateFuncMap() template.FuncMap {
	// Start with the public fleet template functions
	funcMap := v1beta1.GetGoTemplateFuncMap()

	// Add internal-only functions for quadlet rendering
	funcMap["contains"] = func(substr string, input any) bool {
		str, ok := input.(string)
		if ok {
			return strings.Contains(str, substr)
		}
		return false
	}

	return funcMap
}

type InstallAction struct {
	Action      ActionType
	Source      string
	Destination string
	Content     string
	Template    bool
	Mode        os.FileMode
}

type ImageConfig struct {
	Image string `mapstructure:"image"`
	Tag   string `mapstructure:"tag"`
}

type RendererConfig struct {
	// UserScope indicates that generated units target a systemd user manager.
	// It is inferred from the selected unit output directory because package
	// builds can render system units while running as an unprivileged builder.
	UserScope bool `mapstructure:"-"`

	// Output directories
	ReadOnlyConfigOutputDir  string `mapstructure:"readonly-config-dir"`
	WriteableConfigOutputDir string `mapstructure:"writeable-config-dir"`
	QuadletFilesOutputDir    string `mapstructure:"quadlet-dir"`
	SystemdUnitOutputDir     string `mapstructure:"systemd-dir"`
	BinOutputDir             string `mapstructure:"bin-dir"`
	VarTmpOutputDir          string `mapstructure:"var-tmp-dir"`
	VarLibOutputDir          string `mapstructure:"var-lib-dir"`
	// GatewayHostPort is the host-facing port published by the gateway.
	GatewayHostPort string `mapstructure:"-"`
	// GatewayHostPortSuffix is appended to gateway URLs; it stays empty when the
	// host-facing port is the standard HTTPS port 443.
	GatewayHostPortSuffix string `mapstructure:"-"`

	// Source directories for binary search
	BinSourceDirs []string `mapstructure:"bin-source-dirs"`

	FlightctlServicesTagOverride string `mapstructure:"flightctl-services-tag-override"`
	FlightctlUiTagOverride       bool   `mapstructure:"flightctl-ui-tag-override"`

	// Images
	Api                ImageConfig `mapstructure:"api"`
	Periodic           ImageConfig `mapstructure:"periodic"`
	Worker             ImageConfig `mapstructure:"worker"`
	DeltaWorker        ImageConfig `mapstructure:"delta-worker"`
	AlertExporter      ImageConfig `mapstructure:"alert-exporter"`
	CliArtifacts       ImageConfig `mapstructure:"cli-artifacts"`
	AlertmanagerProxy  ImageConfig `mapstructure:"alertmanager-proxy"`
	PamIssuer          ImageConfig `mapstructure:"pam-issuer"`
	Ui                 ImageConfig `mapstructure:"ui"`
	DbSetup            ImageConfig `mapstructure:"db-setup"`
	Db                 ImageConfig `mapstructure:"db"`
	Kv                 ImageConfig `mapstructure:"kv"`
	Alertmanager       ImageConfig `mapstructure:"alertmanager"`
	ImagebuilderApi    ImageConfig `mapstructure:"imagebuilder-api"`
	ImagebuilderWorker ImageConfig `mapstructure:"imagebuilder-worker"`
	Grafana            ImageConfig `mapstructure:"grafana"`
	Prometheus         ImageConfig `mapstructure:"prometheus"`
	TelemetryGateway   ImageConfig `mapstructure:"telemetry-gateway"`
	UserinfoProxy      ImageConfig `mapstructure:"userinfo-proxy"`
	Gateway            ImageConfig `mapstructure:"gateway"`
	RemoteAccess       ImageConfig `mapstructure:"remote-access"`
}

func NewRendererConfig() *RendererConfig {
	config := &RendererConfig{
		ReadOnlyConfigOutputDir:  "/usr/share/flightctl",
		WriteableConfigOutputDir: "/etc/flightctl",
		QuadletFilesOutputDir:    "/usr/share/containers/systemd",
		SystemdUnitOutputDir:     "/usr/lib/systemd/system",
		BinOutputDir:             "/usr/bin",
		VarTmpOutputDir:          "/var/tmp",
		VarLibOutputDir:          "/var/lib",
		GatewayHostPort:          "443",
		GatewayHostPortSuffix:    "",
	}

	if os.Geteuid() != 0 {
		config.UserScope = true
		homeDir, err := os.UserHomeDir()
		if err != nil || homeDir == "" {
			homeDir = os.Getenv("HOME")
		}
		if homeDir == "" {
			homeDir = "."
		}

		xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
		if xdgConfigHome == "" {
			xdgConfigHome = filepath.Join(homeDir, ".config")
		}
		xdgDataHome := os.Getenv("XDG_DATA_HOME")
		if xdgDataHome == "" {
			xdgDataHome = filepath.Join(homeDir, ".local", "share")
		}
		xdgCacheHome := os.Getenv("XDG_CACHE_HOME")
		if xdgCacheHome == "" {
			xdgCacheHome = filepath.Join(homeDir, ".cache")
		}
		xdgStateHome := os.Getenv("XDG_STATE_HOME")
		if xdgStateHome == "" {
			xdgStateHome = filepath.Join(homeDir, ".local", "state")
		}

		config.ReadOnlyConfigOutputDir = filepath.Join(xdgDataHome, "flightctl")
		config.WriteableConfigOutputDir = filepath.Join(xdgConfigHome, "flightctl")
		config.QuadletFilesOutputDir = filepath.Join(xdgConfigHome, "containers", "systemd")
		config.SystemdUnitOutputDir = filepath.Join(xdgConfigHome, "systemd", "user")
		config.BinOutputDir = filepath.Join(xdgDataHome, "flightctl", "bin")
		config.VarTmpOutputDir = filepath.Join(xdgCacheHome, "flightctl", "tmp")
		config.VarLibOutputDir = xdgStateHome
		config.GatewayHostPort = "9443"
		config.GatewayHostPortSuffix = ":9443"
	}

	// Keep the renderer's defaults in sync with deploy/scripts/shared.sh while
	// still allowing deployment helpers to use explicit paths.
	if value := os.Getenv("CONFIG_READONLY_DIR"); value != "" {
		config.ReadOnlyConfigOutputDir = value
	}
	if value := os.Getenv("CONFIG_WRITEABLE_DIR"); value != "" {
		config.WriteableConfigOutputDir = value
	}
	if value := os.Getenv("QUADLET_FILES_OUTPUT_DIR"); value != "" {
		config.QuadletFilesOutputDir = value
	}
	if value := os.Getenv("SYSTEMD_UNIT_OUTPUT_DIR"); value != "" {
		config.SystemdUnitOutputDir = value
	}
	if value := os.Getenv("BIN_OUTPUT_DIR"); value != "" {
		config.BinOutputDir = value
	}
	if value := os.Getenv("VAR_TMP_OUTPUT_DIR"); value != "" {
		config.VarTmpOutputDir = value
	}
	if value := os.Getenv("VAR_LIB_OUTPUT_DIR"); value != "" {
		config.VarLibOutputDir = value
	}
	return config
}

func findBinarySource(binaryName string, searchDirs []string) (string, error) {
	for _, dir := range searchDirs {
		path := filepath.Join(dir, binaryName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("binary %q not found in directories: %v", binaryName, searchDirs)
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

		case ActionCopyBinary:
			sourcePath, err := findBinarySource(action.Source, config.BinSourceDirs)
			if err != nil {
				return fmt.Errorf("failed to find binary %s: %w", action.Source, err)
			}
			if err := processFile(sourcePath, action.Destination, action.Template, action.Mode, config); err != nil {
				return fmt.Errorf("failed to process binary %s: %w", action.Source, err)
			}
			log.Infof("Processed binary: %s -> %s (found at %s)", action.Source, action.Destination, sourcePath)

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

		case ActionWriteFile:
			if err := writeRenderedFile(action.Destination, action.Content, action.Mode); err != nil {
				return fmt.Errorf("failed to write file %s: %w", action.Destination, err)
			}
			log.Infof("Wrote file: %s", action.Destination)

		case ActionCreateSymlink:
			if err := createSymlink(action.Source, action.Destination); err != nil {
				return fmt.Errorf("failed to create symlink %s: %w", action.Destination, err)
			}
			log.Infof("Created symlink: %s -> %s", action.Destination, action.Source)

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
		tmpl, err := template.New(filepath.Base(sourcePath)).Funcs(getQuadletTemplateFuncMap()).Parse(string(content))
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
	if err := os.Chmod(destPath, mode); err != nil {
		return fmt.Errorf("failed to set destination file permissions: %w", err)
	}

	return nil
}

func writeRenderedFile(destPath, content string, mode os.FileMode) error {
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, ExecutableFileMode); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}
	if err := os.WriteFile(destPath, []byte(content), mode); err != nil {
		return fmt.Errorf("failed to write destination file: %w", err)
	}
	if err := os.Chmod(destPath, mode); err != nil {
		return fmt.Errorf("failed to set destination file permissions: %w", err)
	}
	return nil
}

func createSymlink(target, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), ExecutableFileMode); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	existingTarget, err := os.Readlink(destPath)
	if err == nil {
		if existingTarget == target {
			return nil
		}
		return fmt.Errorf("destination already links to %s", existingTarget)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect destination: %w", err)
	}

	if err := os.Symlink(target, destPath); err != nil {
		return fmt.Errorf("failed to create link: %w", err)
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
	config.DeltaWorker.Tag = tag
	config.AlertExporter.Tag = tag
	config.CliArtifacts.Tag = tag
	config.AlertmanagerProxy.Tag = tag
	config.PamIssuer.Tag = tag
	config.DbSetup.Tag = tag
	config.ImagebuilderApi.Tag = tag
	config.ImagebuilderWorker.Tag = tag
	config.TelemetryGateway.Tag = tag
	config.UserinfoProxy.Tag = tag

	config.RemoteAccess.Tag = tag

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
	config.selectUnitScope()
	if os.Geteuid() == 0 && config.UserScope {
		return fmt.Errorf("UID 0 selects system-scope Quadlets; user-scope output paths cannot be deployed as root")
	}
	if err := config.validateSpecifierPaths(); err != nil {
		return err
	}
	if config.UserScope {
		// The writable configuration tree contains credentials and private keys.
		// Keep it private even when XDG_CONFIG_HOME points to a shared directory.
		if err := os.MkdirAll(config.WriteableConfigOutputDir, 0700); err != nil {
			return fmt.Errorf("failed to create private writable configuration directory %s: %w", config.WriteableConfigOutputDir, err)
		}
		if err := os.Chmod(config.WriteableConfigOutputDir, 0700); err != nil {
			return fmt.Errorf("failed to restrict writable configuration directory %s: %w", config.WriteableConfigOutputDir, err)
		}
	}

	config.ApplyFlightctlServicesTagOverride(log)

	manifest := servicesManifest(config)
	if err := processInstallManifest(manifest, config, log); err != nil {
		return fmt.Errorf("failed to process install manifest: %w", err)
	}

	log.Info("Installation complete")
	return nil
}

// selectUnitScope derives the systemd scope from the unit output directory.
// This keeps normal rootless installs tied to ~/.config/systemd/user while
// allowing RPM builds to render system units from an unprivileged mockbuild
// process into %{buildroot}/usr/lib/systemd/system.
func (config *RendererConfig) selectUnitScope() {
	unitDir := filepath.Clean(config.SystemdUnitOutputDir)
	if filepath.Base(filepath.Dir(unitDir)) == "systemd" {
		switch filepath.Base(unitDir) {
		case "user":
			config.UserScope = true
		case "system":
			config.UserScope = false
		}
	}

	if config.UserScope {
		config.GatewayHostPort = "9443"
		config.GatewayHostPortSuffix = ":9443"
	} else {
		config.GatewayHostPort = "443"
		config.GatewayHostPortSuffix = ""
	}
}

// validateSpecifierPaths rejects independent output directory overrides that
// disagree with the paths embedded in the shared Quadlet sources. Users can
// relocate user-scope roots through XDG_*; package builds can stage system
// paths below their buildroot.
func (config *RendererConfig) validateSpecifierPaths() error {
	if config.UserScope {
		homeDir := rendererHomeDir()
		configHome := xdgDirectory("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))
		dataHome := xdgDirectory("XDG_DATA_HOME", filepath.Join(homeDir, ".local", "share"))
		stateHome := xdgDirectory("XDG_STATE_HOME", filepath.Join(homeDir, ".local", "state"))
		if !filepath.IsAbs(configHome) || !filepath.IsAbs(dataHome) || !filepath.IsAbs(stateHome) {
			return fmt.Errorf("XDG_CONFIG_HOME, XDG_DATA_HOME, and XDG_STATE_HOME must be absolute paths for user-scope Quadlets")
		}

		for _, path := range []struct {
			label    string
			actual   string
			expected string
		}{
			{"writeable config directory", config.WriteableConfigOutputDir, filepath.Join(configHome, "flightctl")},
			{"read-only config directory", config.ReadOnlyConfigOutputDir, filepath.Join(dataHome, "flightctl")},
			{"binary directory", config.BinOutputDir, filepath.Join(dataHome, "flightctl", "bin")},
			{"Quadlet directory", config.QuadletFilesOutputDir, filepath.Join(configHome, "containers", "systemd")},
			{"systemd unit directory", config.SystemdUnitOutputDir, filepath.Join(configHome, "systemd", "user")},
		} {
			if err := requireOutputPath(path.label, path.actual, path.expected); err != nil {
				return err
			}
		}
		if !filepath.IsAbs(config.VarTmpOutputDir) || !filepath.IsAbs(config.VarLibOutputDir) {
			return fmt.Errorf("temporary and state output directories must be absolute paths")
		}
		if err := requireOutputPath("state directory", config.VarLibOutputDir, stateHome); err != nil {
			return err
		}
		return nil
	}

	buildroot := systemdBuildroot(config.SystemdUnitOutputDir)
	unitPath := unstageSystemPath(config.SystemdUnitOutputDir, buildroot)
	if unitPath != "/usr/lib/systemd/system" && unitPath != "/etc/systemd/system" {
		return fmt.Errorf("systemd unit directory %q is not a system-scope unit path", config.SystemdUnitOutputDir)
	}
	quadletPath := unstageSystemPath(config.QuadletFilesOutputDir, buildroot)
	if quadletPath != "/usr/share/containers/systemd" && quadletPath != "/etc/containers/systemd" && quadletPath != "/usr/lib/containers/systemd" {
		return fmt.Errorf("Quadlet directory %q is not a system Quadlet search path", config.QuadletFilesOutputDir)
	}

	for _, path := range []struct {
		label    string
		actual   string
		expected string
	}{
		{"writeable config directory", config.WriteableConfigOutputDir, systemPath(buildroot, "etc", "flightctl")},
		{"read-only config directory", config.ReadOnlyConfigOutputDir, systemPath(buildroot, "usr", "share", "flightctl")},
		{"binary directory", config.BinOutputDir, systemPath(buildroot, "usr", "bin")},
		{"state directory", config.VarLibOutputDir, systemPath(buildroot, "var", "lib")},
	} {
		if err := requireOutputPath(path.label, path.actual, path.expected); err != nil {
			return err
		}
	}
	if !filepath.IsAbs(config.VarTmpOutputDir) {
		return fmt.Errorf("temporary output directory %q must be absolute", config.VarTmpOutputDir)
	}
	return nil
}

func requireOutputPath(label, actual, expected string) error {
	if filepath.Clean(actual) != filepath.Clean(expected) {
		return fmt.Errorf("%s %q does not match the path %q used by the shared Quadlet units", label, actual, expected)
	}
	return nil
}

func rendererHomeDir() string {
	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		return homeDir
	}
	if homeDir = os.Getenv("HOME"); homeDir != "" {
		return homeDir
	}
	return "."
}

func xdgDirectory(variable, fallback string) string {
	if value := os.Getenv(variable); value != "" {
		return value
	}
	return fallback
}

func systemdBuildroot(systemdUnitDir string) string {
	unitDir := filepath.Clean(systemdUnitDir)
	suffix := filepath.Join("usr", "lib", "systemd", "system")
	if !strings.HasSuffix(unitDir, string(filepath.Separator)+suffix) {
		return ""
	}
	buildroot := strings.TrimSuffix(unitDir, suffix)
	buildroot = strings.TrimSuffix(buildroot, string(filepath.Separator))
	return buildroot
}

func systemPath(buildroot string, elements ...string) string {
	path := filepath.Join(append([]string{string(filepath.Separator)}, elements...)...)
	if buildroot == "" {
		return path
	}
	return filepath.Join(buildroot, strings.TrimPrefix(path, string(filepath.Separator)))
}

func unstageSystemPath(path, buildroot string) string {
	path = filepath.Clean(path)
	if buildroot == "" {
		return path
	}
	prefix := filepath.Clean(buildroot) + string(filepath.Separator)
	if !strings.HasPrefix(path, prefix) {
		return path
	}
	return string(filepath.Separator) + strings.TrimPrefix(path, prefix)
}
