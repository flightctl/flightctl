package identity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// SystemdCredsCommand is the command name for systemd-creds utility
	SystemdCredsCommand = "systemd-creds"
	// FlightctlAgentServiceName is the systemd service name for the flightctl agent
	FlightctlAgentServiceName = "flightctl-agent"
)

// Sealer handles sealing of secrets for secure storage
type Sealer interface {
	// Seal seals a secret using systemd-creds and TPM2
	// serviceName: the systemd service that can unseal this credential
	// sealKeyType: the type of sealing key (host, tpm2, host+tpm2)
	// The credential name and output path are generated based on the service name
	Seal(ctx context.Context, serviceName string, sealKeyType SealKeyType, secret []byte) error
	// VerifyFromPath verifies a sealed secret can be decrypted from a given path
	VerifyFromPath(ctx context.Context, sealedPath string) error
}

// sealer implements the Sealer interface
type sealer struct {
	log     *log.PrefixLogger
	rw      fileio.ReadWriter
	exec    executer.Executer
	systemd *client.Systemd
}

// NewSealer creates a new sealer
func NewSealer(log *log.PrefixLogger, rw fileio.ReadWriter, exec executer.Executer) (Sealer, error) {
	return &sealer{
		log:     log,
		rw:      rw,
		exec:    exec,
		systemd: client.NewSystemd(exec),
	}, nil
}

// Seal seals a secret using systemd-creds and TPM2
func (s *sealer) Seal(ctx context.Context, serviceName string, sealKeyType SealKeyType, secret []byte) error {
	if len(secret) == 0 {
		return fmt.Errorf("secret cannot be empty")
	}

	if serviceName == "" {
		return fmt.Errorf("service name is required")
	}

	var credentialName, outputPath string
	if serviceName == FlightctlAgentServiceName {
		credentialName = TPMStorageCredentialName
		outputPath = ParentCredentialPath
	} else {
		credentialName = fmt.Sprintf("%s-password", serviceName)
		outputPath = filepath.Join(ChildCredentialDir, fmt.Sprintf("%s.sealed", serviceName))
	}

	outputDir := filepath.Dir(outputPath)
	if err := s.rw.MkdirAll(outputDir, 0700); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}

	tempFile, err := createSecureTempFile(secret, s.rw, s.exec)
	if err != nil {
		return fmt.Errorf("failed to create temporary secret file: %w", err)
	}
	defer cleanup(tempFile, s.rw)

	s.log.Infof("Using %s sealing for service %s", sealKeyType, serviceName)
	s.log.Debugf("Sealing credential %s to %s with key type %s", credentialName, outputPath, sealKeyType)

	if err := s.systemd.CredsEncrypt(ctx, credentialName, sealKeyType.String(), tempFile, outputPath); err != nil {
		return fmt.Errorf("%w: %v", ErrSealingFailed, err)
	}

	// verify the sealed file was created
	exists, err := s.rw.PathExists(outputPath)
	if err != nil || !exists {
		return fmt.Errorf("sealed file not created: %w", err)
	}

	// ensure secure permissions
	data, err := s.rw.ReadFile(outputPath)
	if err != nil {
		return fmt.Errorf("reading sealed file: %w", err)
	}
	if err := s.rw.WriteFile(outputPath, data, 0400); err != nil {
		return fmt.Errorf("setting secure permissions on sealed file: %w", err)
	}

	s.log.Infof("Successfully sealed secret for service %s (%d bytes)", serviceName, len(data))
	s.log.Info("This credential can ONLY be unsealed by the specified service")

	return nil
}

// VerifyFromPath verifies a sealed secret can be decrypted from a given path
func (s *sealer) VerifyFromPath(ctx context.Context, sealedPath string) error {
	exists, err := s.rw.PathExists(sealedPath)
	if err != nil || !exists {
		return fmt.Errorf("sealed file not found: %w", err)
	}

	// try to decrypt
	_, err = s.systemd.CredsDecrypt(ctx, sealedPath)
	if err != nil {
		return fmt.Errorf("failed to verify sealed password: %w", err)
	}

	s.log.Debug("Sealed password verified successfully")
	return nil
}

// createSecureTempFile creates a temporary file with the data and returns the path.
// It attempts to use shared memory (RAM) first for security, falling back to the system temp directory.
// The caller is responsible for removing the file.
func createSecureTempFile(data []byte, rw fileio.ReadWriter, exec executer.Executer) (string, error) {
	var tempFile *os.File
	var err error

	// try shared memory (RAM) first for security
	tempFile, err = exec.TempFile(SharedMemoryPrefix, "tpm-pass-*.tmp")
	if err != nil {
		tempFile, err = exec.TempFile("", "tpm-pass-*.tmp")
	}

	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	tempPath := tempFile.Name()
	tempFile.Close()

	// Write through fileio with secure permissions
	if err := rw.WriteFile(tempPath, data, 0600); err != nil {
		_ = rw.RemoveFile(tempPath) // best effort cleanup
		return "", fmt.Errorf("failed to write to temp file: %w", err)
	}

	return tempPath, nil
}

// cleanup securely removes a temporary file
func cleanup(path string, rw fileio.ReadWriter) {
	if path == "" {
		return
	}

	if err := rw.OverwriteAndWipe(path); err != nil {
		_ = rw.RemoveFile(path) // best effort cleanup
	}
}
