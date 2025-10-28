package renderservices

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
)

const configFile = "/etc/flightctl/service-config.yaml"
const envTemplateFile = "/usr/share/flightctl/flightctl-ui/env.template"
const envOutputFile = "/etc/flightctl/flightctl-ui/env"
const certsSourceDir = "/etc/flightctl/pki"
const uiCertsVolumeName = "flightctl-ui-certs"

func RenderServicesConfig() error {
	fmt.Println("Rendering services config file")

	config, err := unmarshalServicesConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Parse the template
	tmpl, err := template.ParseFiles(envTemplateFile)
	if err != nil {
		return fmt.Errorf("failed to parse template file %s: %w", envTemplateFile, err)
	}

	// Ensure output directory exists
	outputDir := filepath.Dir(envOutputFile)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}

	// Create output file
	outputFile, err := os.Create(envOutputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file %s: %w", envOutputFile, err)
	}
	defer outputFile.Close()

	// Execute template with config directly
	if err := tmpl.Execute(outputFile, config); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	fmt.Printf("Successfully rendered template to %s\n", envOutputFile)

	// Copy certificates to the UI volume
	if err := copyCertificatesToVolume(); err != nil {
		return fmt.Errorf("failed to copy certificates to volume: %w", err)
	}

	return nil
}

// copyCertificatesToVolume copies certificates from the host to the flightctl-ui-certs volume
func copyCertificatesToVolume() error {
	fmt.Println("Copying certificates to flightctl-ui-certs volume")

	// Create logger - using a simple console logger since this is a command-line tool
	logger := log.NewPrefixLogger("render-services")

	// Create executer for running podman commands
	executer := &executer.CommonExecuter{}

	// Create file reader/writer
	readWriter := fileio.NewReadWriter()

	// Create poll config for retries
	pollConfig := poll.Config{
		MaxDelay:     30 * time.Second,
		BaseDelay:    5 * time.Second,
		Factor:       1.5,
		MaxSteps:     5,
		JitterFactor: 0.1,
	}

	// Create podman client
	podmanClient := client.NewPodman(logger, executer, readWriter, pollConfig)

	ctx := context.Background()

	// Get the volume mount point
	volumePath, err := podmanClient.InspectVolumeMount(ctx, uiCertsVolumeName)
	if err != nil {
		return fmt.Errorf("failed to inspect volume %s: %w", uiCertsVolumeName, err)
	}

	fmt.Printf("Volume %s is mounted at: %s\n", uiCertsVolumeName, volumePath)

	// Copy server certificate
	serverCertSrc := filepath.Join(certsSourceDir, "server.crt")
	serverCertDst := filepath.Join(volumePath, "server.crt")
	if err := copyFile(serverCertSrc, serverCertDst); err != nil {
		return fmt.Errorf("failed to copy server certificate: %w", err)
	}
	fmt.Printf("Copied %s to %s\n", serverCertSrc, serverCertDst)

	// Copy server key
	serverKeySrc := filepath.Join(certsSourceDir, "server.key")
	serverKeyDst := filepath.Join(volumePath, "server.key")
	if err := copyFile(serverKeySrc, serverKeyDst); err != nil {
		return fmt.Errorf("failed to copy server key: %w", err)
	}
	fmt.Printf("Copied %s to %s\n", serverKeySrc, serverKeyDst)

	// Copy auth CA certificate if it exists
	authCASrc := filepath.Join(certsSourceDir, "auth", "ca.crt")
	authCADst := filepath.Join(volumePath, "ca_auth.crt")
	if _, err := os.Stat(authCASrc); err == nil {
		if err := copyFile(authCASrc, authCADst); err != nil {
			return fmt.Errorf("failed to copy auth CA certificate: %w", err)
		}
		fmt.Printf("Copied %s to %s\n", authCASrc, authCADst)
	} else {
		fmt.Printf("Auth CA certificate not found at %s, skipping\n", authCASrc)
	}

	fmt.Println("Successfully copied certificates to volume")
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
