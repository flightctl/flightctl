package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
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
		workDirCleanup()
		return "", func() {}, fmt.Errorf("flightctl-backup failed: %w", err)
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
