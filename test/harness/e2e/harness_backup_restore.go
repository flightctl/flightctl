package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/quadlet"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/util"
	ginkgo "github.com/onsi/ginkgo/v2"
)

const (
	vmBackupBinaryPath  = "/tmp/flightctl-backup"
	vmRestoreBinaryPath = "/tmp/flightctl-restore"
	vmRestoreArchive    = "/tmp/flightctl-restore.tar.gz"
)

// BackupRestore performs backup/restore operations using the production flightctl-backup
// and flightctl-restore binaries. The binaries read credentials from the deployment
// environment (K8s Secrets or /etc/flightctl/service-config.yaml for Podman).
// Create with Harness.NewBackupRestore.
type BackupRestore struct {
	*Harness
	providers *infra.Providers
}

// NewBackupRestore returns a BackupRestore that uses this harness and the given providers.
// If p is nil, setup.GetDefaultProviders() is used.
func (h *Harness) NewBackupRestore(p *infra.Providers) *BackupRestore {
	if p == nil {
		p = setup.GetDefaultProviders()
	}
	return &BackupRestore{Harness: h, providers: p}
}

// VerifyAllServicesRunning verifies that all FlightCtl services that are stopped during restore
// are back up and running. This includes all 8 services: API, worker, periodic, imagebuilder-api,
// imagebuilder-worker, gateway, alert-exporter, and alertmanager-proxy.
func (br *BackupRestore) VerifyAllServicesRunning() error {
	services := []infra.ServiceName{
		infra.ServiceAPI,
		infra.ServiceWorker,
		infra.ServicePeriodic,
		infra.ServiceImageBuilderAPI,
		infra.ServiceImageBuilderWorker,
		infra.ServiceTelemetryGateway,
		infra.ServiceAlertExporter,
		infra.ServiceAlertmanagerProxy,
	}
	for _, svc := range services {
		running, err := br.providers.Lifecycle.IsRunning(svc)
		if err != nil {
			return fmt.Errorf("failed to check if %s is running: %w", svc, err)
		}
		if !running {
			return fmt.Errorf("service %s is not running", svc)
		}
	}
	return nil
}

// ScaleUpFlightCtlServices scales API, worker, periodic, alert-exporter, and alertmanager-proxy
// back up to 1 replica. Used as a safety net after restore in case the binary is killed before
// its own deferred startup.
func (br *BackupRestore) ScaleUpFlightCtlServices() error {
	services := []infra.ServiceName{
		infra.ServiceAPI,
		infra.ServiceWorker,
		infra.ServicePeriodic,
		infra.ServiceAlertExporter,
		infra.ServiceAlertmanagerProxy,
	}
	for _, svc := range services {
		if err := br.providers.Lifecycle.Start(svc); err != nil {
			return fmt.Errorf("scale up %s: %w", svc, err)
		}
	}
	for _, svc := range services {
		if err := br.providers.Lifecycle.WaitForReady(svc, 2*time.Minute); err != nil {
			return fmt.Errorf("wait for ready %s: %w", svc, err)
		}
	}
	return nil
}

// RunFlightCtlBackup runs the flightctl-backup binary.
// For quadlet: copies binary to the VM via base64, runs there, downloads archive via base64.
// For K8s: runs locally (binary uses kubeconfig to connect to cluster).
// The binary auto-detects deployment type and reads credentials from the environment.
func (br *BackupRestore) RunFlightCtlBackup(outputDir string) (archivePath string, checksumPath string, err error) {
	backupBinary := br.GetFlightctlBackupPath()
	if _, statErr := os.Stat(backupBinary); os.IsNotExist(statErr) {
		return "", "", fmt.Errorf("binary not found: %s (build with: make flightctl-backup)", backupBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	if br.providers.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
		return br.runBackupOnQuadlet(ctx, backupBinary, outputDir)
	}

	args := []string{"--output", outputDir}
	if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
		args = append(args, "--namespace", ns)
	}
	if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
		args = append(args, "--internal-namespace", ns)
	}
	backupCmd := exec.CommandContext(ctx, backupBinary, args...)
	backupCmd.Stdout = ginkgo.GinkgoWriter
	backupCmd.Stderr = ginkgo.GinkgoWriter
	if err := backupCmd.Run(); err != nil {
		return "", "", fmt.Errorf("flightctl-backup failed: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(outputDir, "flightctl-backup-*.tar.gz"))
	if err != nil {
		return "", "", fmt.Errorf("glob backup archives: %w", err)
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no backup archive found in %s", outputDir)
	}

	archivePath = matches[0]
	checksumPath = archivePath + ".sha256"
	return archivePath, checksumPath, nil
}

// RunFlightCtlBackupRaw runs the flightctl-backup binary with the given arguments.
// Useful for negative tests (bad flags, missing args, etc.).
// Returns combined stdout+stderr and any execution error.
func (br *BackupRestore) RunFlightCtlBackupRaw(args ...string) (output string, err error) {
	backupBinary := br.GetFlightctlBackupPath()
	if _, statErr := os.Stat(backupBinary); os.IsNotExist(statErr) {
		return "", fmt.Errorf("binary not found: %s (build with: make flightctl-backup)", backupBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	cmd := exec.CommandContext(ctx, backupBinary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunFlightCtlRestoreRaw runs the flightctl-restore binary with the given arguments.
// Useful for negative tests (bad args, corrupted archives, etc.).
// Returns combined stdout+stderr and any execution error.
func (br *BackupRestore) RunFlightCtlRestoreRaw(args ...string) (output string, err error) {
	restoreBinary := br.GetFlightctlRestorePath()
	if _, statErr := os.Stat(restoreBinary); os.IsNotExist(statErr) {
		return "", fmt.Errorf("binary not found: %s (build with: make flightctl-restore)", restoreBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	cmd := exec.CommandContext(ctx, restoreBinary, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunFlightCtlRestoreFromArchive runs the flightctl-restore binary with the given archive path.
// For quadlet: copies binary+archive to the VM via base64, runs there.
// For K8s: runs locally (binary uses kubeconfig to connect to cluster).
// The binary reads credentials from the environment and handles service stop/start internally.
func (br *BackupRestore) RunFlightCtlRestoreFromArchive(archivePath string) error {
	restoreBinary := br.GetFlightctlRestorePath()
	if _, err := os.Stat(restoreBinary); os.IsNotExist(err) {
		return fmt.Errorf("binary not found: %s (build with: make flightctl-restore)", restoreBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	if br.providers.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
		return br.runRestoreOnQuadlet(ctx, restoreBinary, archivePath)
	}

	args := []string{archivePath}
	if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
		args = append(args, "--namespace", ns)
	}
	if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
		args = append(args, "--internal-namespace", ns)
	}
	restoreCmd := exec.CommandContext(ctx, restoreBinary, args...)
	restoreCmd.Stdout = ginkgo.GinkgoWriter
	restoreCmd.Stderr = ginkgo.GinkgoWriter
	if err := restoreCmd.Run(); err != nil {
		return fmt.Errorf("flightctl-restore failed: %w", err)
	}
	return nil
}

// --- Quadlet helpers (binary copy via base64) ---

// runBackupOnQuadlet copies the backup binary to the quadlet VM, runs it there,
// and downloads the resulting archive and checksum via base64 encoding.
func (br *BackupRestore) runBackupOnQuadlet(ctx context.Context, backupBinary, outputDir string) (string, string, error) {
	quadletProvider, ok := br.providers.Infra.(*quadlet.InfraProvider)
	if !ok {
		return "", "", fmt.Errorf("expected quadlet provider but got different type")
	}

	vmOutputDir := fmt.Sprintf("/tmp/flightctl-backup-%d", time.Now().UnixNano())
	vmArgs := []string{"--output", vmOutputDir, "--deployment-type", "podman"}
	if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
		vmArgs = append(vmArgs, "--namespace", ns)
	}
	if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
		vmArgs = append(vmArgs, "--internal-namespace", ns)
	}

	if err := copyBinaryToVM(ctx, quadletProvider, backupBinary, vmBackupBinaryPath); err != nil {
		return "", "", err
	}

	if _, err := quadletProvider.RunCommandContext(ctx, "mkdir", "-p", vmOutputDir); err != nil {
		return "", "", fmt.Errorf("failed to create output dir in VM: %w", err)
	}

	backupCmdArgs := append([]string{vmBackupBinaryPath}, vmArgs...)
	output, err := quadletProvider.RunCommandContext(ctx, backupCmdArgs...)
	fmt.Fprint(ginkgo.GinkgoWriter, output)
	if err != nil {
		return "", "", fmt.Errorf("flightctl-backup failed: %w", err)
	}

	findOutput, err := quadletProvider.RunCommandContext(ctx, "find", vmOutputDir, "-name", "*.tar.gz", "-print", "-quit")
	if err != nil {
		return "", "", fmt.Errorf("failed to find backup archive: %w", err)
	}
	vmArchive := strings.TrimSpace(findOutput)
	if vmArchive == "" {
		return "", "", fmt.Errorf("no backup archive found in %s", vmOutputDir)
	}

	hostArchivePath := filepath.Join(outputDir, filepath.Base(vmArchive))
	if err := downloadFileFromVM(ctx, quadletProvider, vmArchive, hostArchivePath, 0600); err != nil {
		return "", "", fmt.Errorf("download archive: %w", err)
	}

	hostChecksumPath := hostArchivePath + ".sha256"
	if err := downloadFileFromVM(ctx, quadletProvider, vmArchive+".sha256", hostChecksumPath, 0644); err != nil { //nolint:gosec // G306: checksum is non-sensitive
		return "", "", fmt.Errorf("download checksum: %w", err)
	}

	return hostArchivePath, hostChecksumPath, nil
}

// runRestoreOnQuadlet copies the restore binary and archive to the quadlet VM, then runs restore there.
func (br *BackupRestore) runRestoreOnQuadlet(ctx context.Context, restoreBinary, archivePath string) error {
	quadletProvider, ok := br.providers.Infra.(*quadlet.InfraProvider)
	if !ok {
		return fmt.Errorf("expected quadlet provider but got different type")
	}

	if err := copyBinaryToVM(ctx, quadletProvider, restoreBinary, vmRestoreBinaryPath); err != nil {
		return err
	}

	archiveContent, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("failed to read backup archive: %w", err)
	}
	if len(archiveContent) == 0 {
		return fmt.Errorf("backup archive %s is empty", archivePath)
	}
	if err := uploadFileToVM(ctx, quadletProvider, archiveContent, vmRestoreArchive); err != nil {
		return fmt.Errorf("failed to copy backup to VM: %w", err)
	}

	checksumContent, err := os.ReadFile(archivePath + ".sha256")
	if err != nil {
		return fmt.Errorf("failed to read checksum file: %w", err)
	}
	if err := uploadFileToVM(ctx, quadletProvider, checksumContent, vmRestoreArchive+".sha256"); err != nil {
		return fmt.Errorf("failed to copy checksum to VM: %w", err)
	}

	restoreCmdArgs := []string{vmRestoreBinaryPath, vmRestoreArchive}
	if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
		restoreCmdArgs = append(restoreCmdArgs, "--namespace", ns)
	}
	if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
		restoreCmdArgs = append(restoreCmdArgs, "--internal-namespace", ns)
	}

	output, err := quadletProvider.RunCommandContext(ctx, restoreCmdArgs...)
	fmt.Fprint(ginkgo.GinkgoWriter, output)
	if err != nil {
		return fmt.Errorf("flightctl-restore failed: %w", err)
	}
	return nil
}

// copyBinaryToVM reads a local binary and copies it to the VM via base64-encoded stdin.
func copyBinaryToVM(ctx context.Context, qp *quadlet.InfraProvider, localPath, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("failed to read binary %s: %w", localPath, err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	cmd := fmt.Sprintf("base64 -d > %s && chmod +x %s", remotePath, remotePath)
	if _, err := qp.RunCommandWithStdinContext(ctx, strings.NewReader(encoded), "sh", "-c", cmd); err != nil {
		return fmt.Errorf("failed to copy binary to VM at %s: %w", remotePath, err)
	}
	return nil
}

// uploadFileToVM uploads raw content to a file on the VM via base64-encoded stdin.
func uploadFileToVM(ctx context.Context, qp *quadlet.InfraProvider, content []byte, remotePath string) error {
	encoded := base64.StdEncoding.EncodeToString(content)
	cmd := fmt.Sprintf("base64 -d > %s", remotePath)
	if _, err := qp.RunCommandWithStdinContext(ctx, strings.NewReader(encoded), "sh", "-c", cmd); err != nil {
		return fmt.Errorf("failed to upload file to VM at %s: %w", remotePath, err)
	}
	return nil
}

// downloadFileFromVM reads a file from the VM via base64 encoding and writes it locally.
func downloadFileFromVM(ctx context.Context, qp *quadlet.InfraProvider, remotePath, localPath string, perm os.FileMode) error {
	base64Output, err := qp.RunCommandContext(ctx, "base64", remotePath)
	if err != nil {
		return fmt.Errorf("failed to encode %s from VM: %w", remotePath, err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Output))
	if err != nil {
		return fmt.Errorf("failed to decode %s: %w", remotePath, err)
	}
	if err := os.WriteFile(localPath, data, perm); err != nil {
		return fmt.Errorf("failed to write %s: %w", localPath, err)
	}
	return nil
}
