package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"
)

// GetAgentConfigDir returns the prepared e2e agent config directory created by make prepare-e2e-test.
func GetAgentConfigDir() string {
	return filepath.Join(util.GetTopLevelDir(), "bin", "agent", "etc", "flightctl")
}

// GetAgentConfigPath returns the prepared agent config.yaml path for e2e tests.
func GetAgentConfigPath(agentConfigDir string) string {
	if agentConfigDir == "" {
		agentConfigDir = GetAgentConfigDir()
	}
	return filepath.Join(agentConfigDir, "config.yaml")
}

// GetAgentCertsDir returns the prepared agent certs directory path for e2e tests.
func GetAgentCertsDir(agentConfigDir string) string {
	if agentConfigDir == "" {
		agentConfigDir = GetAgentConfigDir()
	}
	return filepath.Join(agentConfigDir, "certs")
}

// AgentConfigDirExists reports whether the prepared agent config and certs are present for package-mode tests.
func AgentConfigDirExists() bool {
	configDir := GetAgentConfigDir()
	configPath := GetAgentConfigPath(configDir)
	certsDir := GetAgentCertsDir(configDir)

	if _, err := os.Stat(configPath); err != nil {
		return false
	}
	if _, err := os.Stat(certsDir); err != nil {
		return false
	}
	return true
}

// CleanupContainerFromPool performs standard harness cleanup for a container-backed device.
// The worker ID is retained for compatibility with callers; container devices are not cached by
// worker ID, so there is no pool entry to remove.
func CleanupContainerFromPool(h *Harness, _ int) {
	h.Cleanup(false)
}

// NewTestHarnessWithContainerPool creates a new test harness with a fresh container-backed device.
// Mirrors NewTestHarnessWithVMPool for the libvirt VM backend; unlike the VM pool, each call
// creates a separate container rather than reusing a device by worker ID.
func NewTestHarnessWithContainerPool(ctx context.Context, workerID int) (*Harness, error) {
	harness, err := newTestHarnessBase(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := harness.GetContainerFromPool(workerID); err != nil {
		harness.ctxCancel()
		return nil, fmt.Errorf("failed to get container device from pool: %w", err)
	}

	return harness, nil
}

// cleanupCurrentContainerDevice removes the current harness device only when it is container
// backed. VM devices remain owned by VMPool and are not deleted here.
func (h *Harness) cleanupCurrentContainerDevice() error {
	device, ok := h.VM.(*vm.ContainerDevice)
	if !ok {
		return nil
	}
	if err := device.ForceDelete(); err != nil {
		return err
	}
	h.VM = nil
	return nil
}

// GetContainerFromPool creates a fresh container-backed device for the given worker ID. The
// container factory intentionally does not cache devices between calls.
func (h *Harness) GetContainerFromPool(workerID int) (vm.TestVMInterface, error) {
	parentCtx := h.GetTestContext()
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, setupSnapshotRestoreTimeout)
	defer cancel()
	return h.getContainerFromPool(ctx, workerID)
}

func (h *Harness) getContainerFromPool(ctx context.Context, workerID int) (*vm.ContainerDevice, error) {
	if err := h.cleanupCurrentContainerDevice(); err != nil {
		return nil, fmt.Errorf("failed to remove previous container device for worker %d: %w", workerID, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	device, err := setupContainerForWorker(ctx, workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create container device for worker %d: %w", workerID, err)
	}
	if err := ctx.Err(); err != nil {
		if cleanupErr := device.ForceDelete(); cleanupErr != nil {
			return nil, errors.Join(err, fmt.Errorf("failed to clean up canceled container device for worker %d: %w", workerID, cleanupErr))
		}
		return nil, err
	}
	h.VM = device
	return device, nil
}

// SetupContainerFromPool creates a fresh container-backed device for this spec. It does not start
// the agent explicitly. Mirrors Harness.SetupVMFromPool's setup role, but containers need no
// snapshot/revert because every request creates a new container from the image.
func (h *Harness) SetupContainerFromPool(workerID int) error {
	parentCtx := h.GetTestContext()
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, setupSnapshotRestoreTimeout)
	defer cancel()
	return h.setupContainerFromPool(ctx, workerID)
}

func (h *Harness) setupContainerFromPool(ctx context.Context, workerID int) (retErr error) {
	device, err := h.getContainerFromPool(ctx, workerID)
	if err != nil {
		return fmt.Errorf("failed to get container device from pool: %w", err)
	}
	defer func() {
		if retErr == nil {
			return
		}
		if cleanupErr := h.cleanupCurrentContainerDevice(); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("failed to clean up container device after setup failure: %w", cleanupErr))
		}
	}()

	if err := device.WaitForSSHToBeReadyContext(ctx); err != nil {
		return fmt.Errorf("failed to wait for container device to be ready: %w", err)
	}

	// Unlike a VM snapshot revert, a freshly recreated container shares the host's kernel CSPRNG
	// state directly (no restored memory image), so there's no entropy-reseed / stale-clock
	// concern here - see SetupVMFromPool's equivalent steps for why VMs need them.

	// Keep enrollment state pristine when using a locally overridden device image.
	if _, err := device.RunSSHContext(ctx, []string{"sudo", "rm", "-f", "/var/lib/flightctl/certs/agent.csr"}, nil); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("container device setup canceled while clearing stale CSR: %w", ctxErr)
		}
		logrus.Warnf("Failed to clean stale CSR: %v", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("container device setup canceled: %w", err)
	}
	printAgentFilesForVMWithContext(ctx, device, "After Fresh Container Start")
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("container device setup canceled: %w", err)
	}
	return nil
}

// SetupContainerFromPoolAndStartAgent creates a fresh container-backed device and starts the
// agent. Mirrors Harness.SetupVMFromPoolAndStartAgent; no container snapshot/revert is needed.
func (h *Harness) SetupContainerFromPoolAndStartAgent(workerID int) error {
	if err := h.SetupContainerFromPool(workerID); err != nil {
		return err
	}
	device := h.VM

	const flightctlAgentStartAttempts = 5
	const flightctlAgentStartRetryDelay = 2 * time.Second

	GinkgoWriter.Printf("🔄 Starting flightctl-agent in fresh container device\n")
	if _, err := device.RunSSH([]string{"sudo", "systemctl", "daemon-reload"}, nil); err != nil {
		logrus.Warnf("daemon-reload before starting flightctl-agent: %v", err)
	}

	var lastErr error
	for attempt := 1; attempt <= flightctlAgentStartAttempts; attempt++ {
		// Best-effort: clears a stale "failed" state from a previous attempt so systemctl start
		// isn't blocked by StartLimitBurst; errors are ignored because reset-failed legitimately
		// fails/no-ops when the unit was never in a failed state (e.g. the first attempt).
		_, _ = device.RunSSH([]string{"sudo", "systemctl", "reset-failed", "flightctl-agent"}, nil)
		_, err := device.RunSSH([]string{"sudo", "systemctl", "start", "flightctl-agent"}, nil)
		if err == nil {
			GinkgoWriter.Printf("✅ flightctl-agent started successfully in container device\n")
			return nil
		}
		lastErr = err
		logrus.Warnf("systemctl start flightctl-agent attempt %d/%d failed: %v", attempt, flightctlAgentStartAttempts, err)
		if attempt == flightctlAgentStartAttempts {
			break
		}
		time.Sleep(flightctlAgentStartRetryDelay)
	}
	return fmt.Errorf("failed to start flightctl-agent after %d attempts: %w", flightctlAgentStartAttempts, lastErr)
}
