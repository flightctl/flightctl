package renderservices

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"
)

const configFile = "/app/service-config.yaml"
const envTemplateFile = "/app/templates/env.template"
const outputDir = "/app/rendered"
const certsSourceDir = "/app/pki"
const uiCertsVolumeName = "/app/ui-certs"

func RenderServicesConfig() error {
	fmt.Println("Rendering services config file")

	config, err := unmarshalServicesConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	tmpl, err := template.ParseFiles(envTemplateFile)
	if err != nil {
		return fmt.Errorf("failed to parse template file %s: %w", envTemplateFile, err)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}

	outputFile, err := os.Create(filepath.Join(outputDir, "env"))
	if err != nil {
		return fmt.Errorf("failed to create output file %s: %w", filepath.Join(outputDir, "env"), err)
	}
	defer outputFile.Close()

	if err := tmpl.Execute(outputFile, config); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	fmt.Printf("Successfully rendered template to %s\n", filepath.Join(outputDir, "env"))

	if err := copyCertificatesToVolume(); err != nil {
		return fmt.Errorf("failed to copy certificates to volume: %w", err)
	}

	return nil
}

func copyCertificatesToVolume() error {
	fmt.Println("Copying certificates to flightctl-ui-certs volume")

	if err := copyFile(filepath.Join(certsSourceDir, "server.crt"), filepath.Join(uiCertsVolumeName, "server.crt")); err != nil {
		return fmt.Errorf("failed to copy server certificate: %w", err)
	}

	if err := copyFile(filepath.Join(certsSourceDir, "server.key"), filepath.Join(uiCertsVolumeName, "server.key")); err != nil {
		return fmt.Errorf("failed to copy server key: %w", err)
	}

	// Copy auth CA certificate if it exists
	if _, err := os.Stat(filepath.Join(certsSourceDir, "auth", "ca.crt")); !os.IsNotExist(err) {
		if err := copyFile(filepath.Join(certsSourceDir, "auth", "ca.crt"), filepath.Join(uiCertsVolumeName, "auth", "ca.crt")); err != nil {
			return fmt.Errorf("failed to copy auth CA certificate: %w", err)
		}
	}

	return nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer srcFile.Close()

	// Ensure destination directory exists
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", dstDir, err)
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", dst, err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}

	return nil
}
