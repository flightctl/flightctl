package e2e

/*
Container Device Pattern - for suites that never switch the device's OS image or reboot it

This mirrors the VM Pool Pattern documented at the top of harness.go, but backs each worker's
device with a plain container (see test/harness/e2e/vm/container.go's ContainerDevice) instead of
a libvirt VM - see the container-backed-device-migration plan for which suites qualify.

1. BEFORE SUITE (once per worker):
   - Call e2e.SetupWorkerHarnessWithContainerDevice() (mirrors SetupWorkerHarnessOrAbort)

2. BEFORE EACH (before each test):
   - Call harness.SetupContainerFromPoolAndStartAgent(workerID) to get a pristine device and start
     the agent (mirrors SetupVMFromPoolAndStartAgent)

3. AFTER SUITE:
   - Container cleanup is handled by make scripts after all tests complete, same as VMs.
*/

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	. "github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"
)

// CleanupContainerFromPool performs standard harness cleanup and removes the container-backed
// device from the global container pool for workerID. Mirrors Cleanup for the VM pool - needed by
// callers (e.g. rollout's horizontal-scale devices) that create devices under custom, test-scoped
// worker IDs which must be freed so a later spec reusing the same worker ID gets a fresh
// container rather than a stale, already-deleted one still cached in the pool.
func CleanupContainerFromPool(h *Harness, workerID int) {
	h.Cleanup(false)
	if err := RemoveContainerFromPool(workerID); err != nil {
		logrus.Warnf("Failed to remove container device from pool: %v", err)
	}
}

// NewTestHarnessWithContainerPool creates a new test harness with a container-backed device from
// the container pool. Mirrors NewTestHarnessWithVMPool for the libvirt VM backend.
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

// GetContainerFromPool retrieves a container-backed device from the pool for the given worker ID.
// Devices are created on-demand if they don't already exist. Mirrors Harness.GetVMFromPool.
func (h *Harness) GetContainerFromPool(workerID int) (vm.TestVMInterface, error) {
	device, err := SetupContainerForWorker(workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get container device from pool for worker %d: %w", workerID, err)
	}
	h.VM = device
	return device, nil
}

// SetupContainerFromPool sets up a container-backed device from the pool and reverts it to a
// pristine state. It does not start the agent. Mirrors Harness.SetupVMFromPool; bounded by the
// same setupSnapshotRestoreTimeout, even though a container revert is far cheaper than a VM
// snapshot restore, to keep the same safety net against an unexpectedly hung runtime call.
func (h *Harness) SetupContainerFromPool(workerID int) error {
	done := make(chan error, 1)
	go func() {
		done <- h.setupContainerFromPoolNoTimeout(workerID)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(setupSnapshotRestoreTimeout):
		return fmt.Errorf("container device revert timed out after %v", setupSnapshotRestoreTimeout)
	}
}

func (h *Harness) setupContainerFromPoolNoTimeout(workerID int) error {
	device, err := h.GetContainerFromPool(workerID)
	if err != nil {
		return fmt.Errorf("failed to get container device from pool: %w", err)
	}

	if err := device.RevertToSnapshot("pristine"); err != nil {
		return fmt.Errorf("failed to revert container device to pristine state: %w", err)
	}

	if err := device.WaitForSSHToBeReady(); err != nil {
		return fmt.Errorf("failed to wait for container device to be ready: %w", err)
	}

	// Unlike a VM snapshot revert, a freshly recreated container shares the host's kernel CSPRNG
	// state directly (no restored memory image), so there's no entropy-reseed / stale-clock
	// concern here - see SetupVMFromPool's equivalent steps for why VMs need them.

	// Clean any stale CSR from a previous container that reused this image layer cache.
	if _, err := device.RunSSH([]string{"sudo", "rm", "-f", "/var/lib/flightctl/certs/agent.csr"}, nil); err != nil {
		logrus.Warnf("Failed to clean stale CSR: %v", err)
	}

	printAgentFilesForVM(device, "After Container Revert")
	return nil
}

// SetupContainerFromPoolAndStartAgent sets up a container-backed device from the pool, reverts it
// to a pristine state, and starts the agent. Mirrors Harness.SetupVMFromPoolAndStartAgent.
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
