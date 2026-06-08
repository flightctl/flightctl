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

// BackupRestore performs backup/restore operations. Create with Harness.NewBackupRestore.
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

// RunFlightCtlBackup runs the flightctl-backup binary. Credentials are read directly from the
// cluster (K8s Secrets or /etc/flightctl/service-config.yaml for Podman). Returns the path to
// the generated archive and a cleanup function that removes the work directory.
func (br *BackupRestore) RunFlightCtlBackup() (archivePath string, cleanup func(), err error) {
	backupBinary := br.GetFlightctlBackupPath()
	if _, err := os.Stat(backupBinary); os.IsNotExist(err) {
		return "", func() {}, fmt.Errorf("binary not found: %s (build with: make flightctl-backup)", backupBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	workDir, err := os.MkdirTemp("", "flightctl-backup-e2e-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create work dir: %w", err)
	}
	workDirCleanup := func() { os.RemoveAll(workDir) }

	outputDir := filepath.Join(workDir, "output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		workDirCleanup()
		return "", func() {}, fmt.Errorf("create output dir: %w", err)
	}

	var backupCmd *exec.Cmd
	// For quadlet deployments, run backup inside the VM.
	// Note: This is test infrastructure automation. External users should manually
	// SSH into their VM and run the backup command there (see error message guidance
	// in internal/backup/deployer.go for user-facing instructions).
	if br.providers.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
		// Type assert to quadlet provider to access RunCommand
		quadletProvider, ok := br.providers.Infra.(*quadlet.InfraProvider)
		if !ok {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("expected quadlet provider but got different type")
		}

		// Use random temp dir inside VM for backup output to avoid cross-test conflicts
		vmOutputDir := fmt.Sprintf("/tmp/flightctl-backup-%d", time.Now().UnixNano())
		vmArgs := []string{"--output", vmOutputDir, "--deployment-type", "podman"}
		if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
			vmArgs = append(vmArgs, "--namespace", ns)
		}
		if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
			vmArgs = append(vmArgs, "--internal-namespace", ns)
		}

		// Run backup inside the VM using the infra provider (handles SSH automatically)
		// All commands use context for proper timeout/cancellation handling

		// Copy flightctl-backup binary from host to VM for testing
		// Edge devices don't have CLI tools pre-installed, but tests need them
		vmBackupBinary := "/tmp/flightctl-backup"
		backupBinaryData, err := os.ReadFile(backupBinary)
		if err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("failed to read backup binary: %w", err)
		}
		encodedBinary := base64.StdEncoding.EncodeToString(backupBinaryData)
		copyCmd := fmt.Sprintf("echo %s | base64 -d > %s && chmod +x %s", encodedBinary, vmBackupBinary, vmBackupBinary)
		if _, err := quadletProvider.RunCommandContext(ctx, "sh", "-c", copyCmd); err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("failed to copy backup binary to VM: %w", err)
		}

		mkdirCmd := []string{"mkdir", "-p", vmOutputDir}
		if _, err := quadletProvider.RunCommandContext(ctx, mkdirCmd...); err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("failed to create output dir in VM: %w", err)
		}

		// Use the binary we just copied to the VM
		backupCmdArgs := append([]string{vmBackupBinary}, vmArgs...)
		output, err := quadletProvider.RunCommandContext(ctx, backupCmdArgs...)
		fmt.Fprint(ginkgo.GinkgoWriter, output)
		if err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("flightctl-backup failed: %w", err)
		}

		// Copy backup archive from VM to host
		// Find the archive name using find command
		findOutput, err := quadletProvider.RunCommandContext(ctx, "find", vmOutputDir, "-name", "*.tar.gz", "-print", "-quit")
		if err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("failed to find backup archive: %w", err)
		}
		vmArchive := strings.TrimSpace(findOutput)
		if vmArchive == "" {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("no backup archive found in %s", vmOutputDir)
		}

		// Read the archive content from VM using base64 encoding to safely transfer binary data
		base64Output, err := quadletProvider.RunCommandContext(ctx, "base64", vmArchive)
		if err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("failed to encode backup archive from VM: %w", err)
		}

		// Decode base64 to get the binary archive data
		archiveBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Output))
		if err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("failed to decode backup archive: %w", err)
		}

		// Write it to the host
		archiveName := filepath.Base(vmArchive)
		hostArchivePath := filepath.Join(outputDir, archiveName)
		if err := os.WriteFile(hostArchivePath, archiveBytes, 0600); err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("failed to write backup archive: %w", err)
		}

		return hostArchivePath, workDirCleanup, nil
	} else {
		args := []string{"--output", outputDir}
		if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
			args = append(args, "--namespace", ns)
		}
		if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
			args = append(args, "--internal-namespace", ns)
		}
		backupCmd = exec.CommandContext(ctx, backupBinary, args...)
		backupCmd.Stdout = ginkgo.GinkgoWriter
		backupCmd.Stderr = ginkgo.GinkgoWriter
		if err := backupCmd.Run(); err != nil {
			workDirCleanup()
			return "", func() {}, fmt.Errorf("flightctl-backup failed: %w", err)
		}
	}

	matches, err := filepath.Glob(filepath.Join(outputDir, "*.tar.gz"))
	if err != nil || len(matches) == 0 {
		workDirCleanup()
		return "", func() {}, fmt.Errorf("no archive found in %s after backup", outputDir)
	}
	return matches[0], workDirCleanup, nil
}

// RunFlightCtlRestore runs the flightctl-restore binary with the given archive path.
// The binary reads credentials from the cluster and handles service stop/start internally.
func (br *BackupRestore) RunFlightCtlRestore(archivePath string) error {
	restoreBinary := br.GetFlightctlRestorePath()
	if _, err := os.Stat(restoreBinary); os.IsNotExist(err) {
		return fmt.Errorf("binary not found: %s (build with: make flightctl-restore)", restoreBinary)
	}

	ctx, cancel := context.WithTimeout(br.Context, util.DURATION_TIMEOUT)
	defer cancel()

	var restoreCmd *exec.Cmd
	// For quadlet deployments, run restore inside the VM.
	// Note: This is test infrastructure automation. External users should manually
	// copy their backup archive to the VM and run restore there.
	if br.providers.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
		// Type assert to quadlet provider to access RunCommand
		quadletProvider, ok := br.providers.Infra.(*quadlet.InfraProvider)
		if !ok {
			return fmt.Errorf("expected quadlet provider but got different type")
		}

		// Copy backup archive from host to VM
		vmArchivePath := "/tmp/flightctl-restore.tar.gz"

		// Read archive from host
		archiveContent, err := os.ReadFile(archivePath)
		if err != nil {
			return fmt.Errorf("failed to read backup archive: %w", err)
		}

		// Copy flightctl-restore binary from host to VM for testing
		// Edge devices don't have CLI tools pre-installed, but tests need them
		vmRestoreBinary := "/tmp/flightctl-restore"
		restoreBinaryData, err := os.ReadFile(restoreBinary)
		if err != nil {
			return fmt.Errorf("failed to read restore binary: %w", err)
		}
		encodedRestoreBinary := base64.StdEncoding.EncodeToString(restoreBinaryData)
		copyRestoreCmd := fmt.Sprintf("echo %s | base64 -d > %s && chmod +x %s", encodedRestoreBinary, vmRestoreBinary, vmRestoreBinary)
		if _, err := quadletProvider.RunCommandContext(ctx, "sh", "-c", copyRestoreCmd); err != nil {
			return fmt.Errorf("failed to copy restore binary to VM: %w", err)
		}

		// Write archive to VM using base64 encoding for safe binary transfer
		// Use context-aware RunCommand for proper timeout/cancellation handling
		encoded := base64.StdEncoding.EncodeToString(archiveContent)
		decodeCmd := fmt.Sprintf("echo %s | base64 -d > %s", encoded, vmArchivePath)
		if _, err := quadletProvider.RunCommandContext(ctx, "sh", "-c", decodeCmd); err != nil {
			return fmt.Errorf("failed to copy backup to VM: %w", err)
		}

		// Run restore inside the VM using the binary we copied
		restoreCmdArgs := []string{vmRestoreBinary, vmArchivePath}
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
	} else {
		args := []string{archivePath}
		if ns := br.providers.Infra.GetExternalNamespace(); ns != "" {
			args = append(args, "--namespace", ns)
		}
		if ns := br.providers.Infra.GetInternalNamespace(); ns != "" {
			args = append(args, "--internal-namespace", ns)
		}
		restoreCmd = exec.CommandContext(ctx, restoreBinary, args...)
	}

	restoreCmd.Stdout = ginkgo.GinkgoWriter
	restoreCmd.Stderr = ginkgo.GinkgoWriter
	if err := restoreCmd.Run(); err != nil {
		return fmt.Errorf("flightctl-restore failed: %w", err)
	}
	return nil
}
