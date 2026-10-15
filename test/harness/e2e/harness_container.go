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
	h.VM = nil
}

// NewTestHarnessWithContainerPool creates a new test harness with a fresh container-backed device.
// Mirrors NewTestHarnessWithVMPool for the libvirt VM backend; unlike the VM pool, each call
// creates a separate container rather than reusing a device by worker ID.
func NewTestHarnessWithContainerPool(ctx context.Context, workerID int) (*Harness, error) {
	harness, err := newTestHarnessBase(ctx)
	if err != nil {
		return nil, err
	}

	setupCtx, cancel := context.WithTimeout(ctx, setupSnapshotRestoreTimeout)
	defer cancel()
	if err := harness.setupContainerFromPoolAndStartAgent(setupCtx, workerID); err != nil {
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
	device, err := h.getContainerFromPool(ctx, workerID)
	if err != nil {
		return nil, err
	}
	h.deferContainerCleanup(device)
	return device, nil
}

func (h *Harness) getContainerFromPool(ctx context.Context, workerID int) (*vm.ContainerDevice, error) {
	if err := h.cleanupCurrentContainerDevice(); err != nil {
		return nil, fmt.Errorf("failed to remove previous container device for worker %d: %w", workerID, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := getOrCreateContainerPool(); err != nil {
		logrus.Warnf("Container device image unavailable: %v", err)
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

func (h *Harness) deferContainerCleanup(device *vm.ContainerDevice) {
	DeferCleanup(func() {
		if err := device.ForceDelete(); err != nil {
			logrus.Warnf("Failed to clean up container device: %v", err)
			return
		}
		if h.VM == device {
			h.VM = nil
		}
	})
}

// SetupContainerFromPool creates a fresh container-backed device for this spec. It does not start
// the agent explicitly. Mirrors Harness.SetupVMFromPool's setup role, but containers need no
// snapshot/revert because every request creates a new container from the image.
func (h *Harness) SetupContainerFromPool(workerID int) error {
	RequireContainerDeviceImage()
	parentCtx := h.GetTestContext()
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, setupSnapshotRestoreTimeout)
	defer cancel()
	if err := h.setupContainerFromPool(ctx, workerID); err != nil {
		return err
	}
	h.deferContainerCleanup(h.VM.(*vm.ContainerDevice))
	return nil
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
func (h *Harness) SetupContainerFromPoolAndStartAgent(workerID int) (retErr error) {
	RequireContainerDeviceImage()
	parentCtx := h.GetTestContext()
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, setupSnapshotRestoreTimeout)
	defer cancel()
	if err := h.setupContainerFromPoolAndStartAgent(ctx, workerID); err != nil {
		return err
	}
	h.deferContainerCleanup(h.VM.(*vm.ContainerDevice))
	return nil
}

// SetupContainerFromPoolWithCurrentOrgAgent is the container-backed counterpart of
// Harness.SetupVMFromPoolWithCurrentOrgAgent: it regenerates the enrollment credentials for the
// currently selected organization and then creates the device container.
//
// Order matters. The VM path restores a pooled guest and afterwards pushes the regenerated files
// onto it (InstallPreparedAgentFilesOnVM), whereas a container device snapshots
// bin/agent/etc/flightctl into the container at creation time (buildAgentIdentityFiles). So the
// config has to be regenerated *before* the container is created; doing it the other way round
// would silently enroll the device into whichever organization the directory happened to hold.
func (h *Harness) SetupContainerFromPoolWithCurrentOrgAgent(workerID int) error {
	RequireContainerDeviceImage()
	if _, err := h.SetupDeviceSimulatorAgentConfig(0, 0); err != nil {
		return fmt.Errorf("preparing agent config for current organization: %w", err)
	}
	return h.SetupContainerFromPoolAndStartAgent(workerID)
}

// SetupDeviceForCurrentSpecWithCurrentOrgAgent dispatches on the current spec's labels like
// SetupDeviceForCurrentSpec, but installs enrollment credentials scoped to the currently selected
// organization on either backend. Multi-organization suites must use this instead of the plain
// dispatcher, otherwise a container-backed spec enrolls into the default organization.
func (h *Harness) SetupDeviceForCurrentSpecWithCurrentOrgAgent(workerID int) error {
	if CurrentSpecUsesContainerDevice() {
		return h.SetupContainerFromPoolWithCurrentOrgAgent(workerID)
	}
	if err := h.SetupVMFromPoolWithCurrentOrgAgent(workerID); err != nil {
		abortVMSetup(workerID, err)
		return err
	}
	return nil
}

func (h *Harness) setupContainerFromPoolAndStartAgent(ctx context.Context, workerID int) (retErr error) {
	if err := h.setupContainerFromPool(ctx, workerID); err != nil {
		return err
	}
	device := h.VM
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, h.cleanupCurrentContainerDevice())
		}
	}()

	const flightctlAgentStartAttempts = 5
	const flightctlAgentStartRetryDelay = 2 * time.Second

	GinkgoWriter.Printf("🔄 Starting flightctl-agent in fresh container device\n")
	if _, err := device.RunSSHContext(ctx, []string{"sudo", "touch", "/run/flightctl-e2e-agent-enabled"}, nil); err != nil {
		return fmt.Errorf("failed to enable explicit agent startup: %w", err)
	}
	if _, err := device.RunSSHContext(ctx, []string{"sudo", "systemctl", "daemon-reload"}, nil); err != nil {
		logrus.Warnf("daemon-reload before starting flightctl-agent: %v", err)
	}

	var lastErr error
	for attempt := 1; attempt <= flightctlAgentStartAttempts; attempt++ {
		// Best-effort: clears a stale "failed" state from a previous attempt so systemctl start
		// isn't blocked by StartLimitBurst; errors are ignored because reset-failed legitimately
		// fails/no-ops when the unit was never in a failed state (e.g. the first attempt).
		_, _ = device.RunSSHContext(ctx, []string{"sudo", "systemctl", "reset-failed", "flightctl-agent"}, nil)
		_, err := device.RunSSHContext(ctx, []string{"sudo", "systemctl", "start", "flightctl-agent"}, nil)
		if err == nil {
			GinkgoWriter.Printf("✅ flightctl-agent started successfully in container device\n")
			return nil
		}
		lastErr = err
		logrus.Warnf("systemctl start flightctl-agent attempt %d/%d failed: %v", attempt, flightctlAgentStartAttempts, err)
		if attempt == flightctlAgentStartAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(flightctlAgentStartRetryDelay):
		}
	}
	return fmt.Errorf("failed to start flightctl-agent after %d attempts: %w", flightctlAgentStartAttempts, lastErr)
}

func RequireContainerDeviceImage() {
	if err := validateContainerDevicePrerequisites(); err != nil {
		logrus.Warnf("Container device image unavailable: %v", err)
		if containerDeviceRequired() {
			AbortSuite(fmt.Sprintf("container device image required but not available: %v", err))
		}
		Skip(fmt.Sprintf("Container device image unavailable; prepare an agent image bundle or set E2E_CONTAINER_DEVICE_IMAGE: %v", err))
	}
}
