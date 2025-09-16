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

var (
	// Per-worker storage
	workerHarnesses sync.Map // map[int]*Harness
	workerContexts  sync.Map // map[int]context.Context
)

// SetupWorkerHarness sets up a VM and harness for the current worker.
// This should be called in BeforeSuite.
func SetupWorkerHarness() (*Harness, context.Context, error) {
	// Get worker number from environment variable
	workerNum := os.Getenv("GINKGO_WORKER_NUM")
	if workerNum == "" {
		return nil, nil, fmt.Errorf("GINKGO_WORKER_NUM environment variable is required but not set")
	}
	// Create composite worker ID: worker + workerNum + "_proc" + GinkgoParallelProcess()
	workerID := fmt.Sprintf("worker%s_proc%d", workerNum, ginkgo.GinkgoParallelProcess())
	logrus.Infof("🔄 [SetupWorkerHarness] Worker %s: Setting up VM and harness", workerID)

	// Create suite context for tracing
	suiteCtx := context.Background() // You can replace this with your tracing setup if needed

	// Setup VM for this worker using the global pool
	_, err := SetupVMForWorker(workerID, os.TempDir(), 2233)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to setup VM for worker %s: %w", workerID, err)
	}

	// Create harness once for the entire worker
	harness, err := NewTestHarnessWithVMPool(suiteCtx, workerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create harness for worker %s: %w", workerID, err)
	}

	// Store the harness and context for this worker
	workerHarnesses.Store(workerID, harness)
	workerContexts.Store(workerID, suiteCtx)

	logrus.Infof("✅ [SetupWorkerHarness] Worker %s: VM and harness setup completed", workerID)
	return harness, suiteCtx, nil
}

// GetWorkerHarness retrieves the harness for the current worker.
// This should be called from your test suite's BeforeEach or tests.
func GetWorkerHarness() *Harness {
	// Get worker number from environment variable
	workerNum := os.Getenv("GINKGO_WORKER_NUM")
	if workerNum == "" {
		ginkgo.Fail("GINKGO_WORKER_NUM environment variable is required but not set")
	}
	// Create composite worker ID: worker + workerNum + "_proc" + GinkgoParallelProcess()
	workerID := fmt.Sprintf("worker%s_proc%d", workerNum, ginkgo.GinkgoParallelProcess())

	h, ok := workerHarnesses.Load(workerID)
	if !ok {
		ginkgo.Fail(fmt.Sprintf("No harness found for worker %s. Make sure SetupWorkerHarness was called in BeforeSuite", workerID))
	}
	return h.(*Harness)
}

// GetWorkerContext retrieves the context for the current worker.
func GetWorkerContext() context.Context {
	// Get worker number from environment variable
	workerNum := os.Getenv("GINKGO_WORKER_NUM")
	if workerNum == "" {
		ginkgo.Fail("GINKGO_WORKER_NUM environment variable is required but not set")
	}
	// Create composite worker ID: worker + workerNum + "_proc" + GinkgoParallelProcess()
	workerID := fmt.Sprintf("worker%s_proc%d", workerNum, ginkgo.GinkgoParallelProcess())

	ctx, ok := workerContexts.Load(workerID)
	if !ok {
		ginkgo.Fail(fmt.Sprintf("No context found for worker %s. Make sure SetupWorkerHarness was called in BeforeSuite", workerID))
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
