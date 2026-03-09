package e2e

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

// E2ESetupAbortExitCode is the exit code used when BeforeSuite setup fails (e.g. VM/base disk missing).
// CI should treat this as job FAILURE; exit 1 (test failures) is treated as UNSTABLE.
const E2ESetupAbortExitCode = 2

// E2ESetupAbortStderrMarker is written to stderr on setup abort so CI can detect it from logs.
const E2ESetupAbortStderrMarker = "FLIGHTCTL_E2E_SETUP_ABORT=1"

var (
	// Per-worker storage
	workerHarnesses sync.Map // map[int]*Harness
	workerContexts  sync.Map // map[int]context.Context
)

// SetupWorkerHarness sets up a VM and harness for the current worker.
// This should be called in BeforeSuite.
func SetupWorkerHarness() (*Harness, context.Context, error) {
	workerID := ginkgo.GinkgoParallelProcess()
	logrus.Infof("🔄 [SetupWorkerHarness] Worker %d: Setting up VM and harness", workerID)

	// Create suite context for tracing
	suiteCtx := context.Background() // You can replace this with your tracing setup if needed

	// Setup VM for this worker using the global pool
	_, err := SetupVMForWorker(workerID, os.TempDir(), 2233)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to setup VM for worker %d: %w", workerID, err)
	}

	// Create harness once for the entire worker
	harness, err := NewTestHarnessWithVMPool(suiteCtx, workerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create harness for worker %d: %w", workerID, err)
	}

	// Store the harness and context for this worker
	workerHarnesses.Store(workerID, harness)
	workerContexts.Store(workerID, suiteCtx)

	logrus.Infof("✅ [SetupWorkerHarness] Worker %d: VM and harness setup completed", workerID)
	return harness, suiteCtx, nil
}

// SetupWorkerHarnessOrAbort calls SetupWorkerHarness and exits the process on error.
// Use in BeforeSuite when a VM is required; the job fails immediately on env problems
// (e.g. base disk not found) so no specs run. Exits with E2ESetupAbortExitCode (2) and
// prints E2ESetupAbortStderrMarker so CI can report FAILURE (not UNSTABLE). Test failures use exit 1.
func SetupWorkerHarnessOrAbort() (*Harness, context.Context) {
	harness, ctx, err := SetupWorkerHarness()
	if err != nil {
		msg := fmt.Sprintf("E2E environment precondition not met: %v\nAborting suite so the job fails immediately (no point running specs).\n", err)
		fmt.Fprint(os.Stderr, msg)
		fmt.Fprint(os.Stderr, E2ESetupAbortStderrMarker+"\n")
		os.Exit(E2ESetupAbortExitCode)
	}
	return harness, ctx
}

// SetupWorkerHarnessWithoutVM sets up a harness for the current worker without VM.
// This is useful for tests that only need API access and don't require a device/agent VM.
// This should be called in BeforeSuite.
func SetupWorkerHarnessWithoutVM() (*Harness, context.Context, error) {
	workerID := ginkgo.GinkgoParallelProcess()
	logrus.Infof("🔄 [SetupWorkerHarnessWithoutVM] Worker %d: Setting up harness without VM", workerID)

	// Create suite context for tracing
	suiteCtx := context.Background()

	// Create harness without VM (no VM pool setup needed)
	harness, err := NewTestHarnessWithoutVM(suiteCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create harness for worker %d: %w", workerID, err)
	}

	// Store the harness and context for this worker
	workerHarnesses.Store(workerID, harness)
	workerContexts.Store(workerID, suiteCtx)

	logrus.Infof("✅ [SetupWorkerHarnessWithoutVM] Worker %d: Harness setup completed (no VM)", workerID)
	return harness, suiteCtx, nil
}

// GetWorkerHarness retrieves the harness for the current worker.
// This should be called from your test suite's BeforeEach or tests.
func GetWorkerHarness() *Harness {
	workerID := ginkgo.GinkgoParallelProcess()
	h, ok := workerHarnesses.Load(workerID)
	if !ok {
		ginkgo.Fail(fmt.Sprintf("No harness found for worker %d. Make sure SetupWorkerHarness or SetupWorkerHarnessWithoutVM was called in BeforeSuite", workerID))
	}
	return h.(*Harness)
}

// GetWorkerContext retrieves the context for the current worker.
func GetWorkerContext() context.Context {
	workerID := ginkgo.GinkgoParallelProcess()
	ctx, ok := workerContexts.Load(workerID)
	if !ok {
		ginkgo.Fail(fmt.Sprintf("No context found for worker %d. Make sure SetupWorkerHarness or SetupWorkerHarnessWithoutVM was called in BeforeSuite", workerID))
	}
	return ctx.(context.Context)
}

// GinkgoBeforeSuite is a convenience function that sets up worker harness in BeforeSuite.
// Use this in your test suite's BeforeSuite if you want a simple setup.
func GinkgoBeforeSuite() {
	var _ = ginkgo.BeforeSuite(func() {
		_, _, err := SetupWorkerHarness()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	})
}
