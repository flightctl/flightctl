package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// GetWorkerHarness retrieves the harness for the current worker.
// This should be called from your test suite's BeforeEach or tests.
func GetWorkerHarness() *Harness {
	workerID := ginkgo.GinkgoParallelProcess()
	h, ok := workerHarnesses.Load(workerID)
	if !ok {
		ginkgo.Fail(fmt.Sprintf("No harness found for worker %d. Make sure SetupWorkerHarness was called in BeforeSuite", workerID))
	}
	return h.(*Harness)
}

// GetWorkerContext retrieves the context for the current worker.
func GetWorkerContext() context.Context {
	workerID := ginkgo.GinkgoParallelProcess()
	ctx, ok := workerContexts.Load(workerID)
	if !ok {
		ginkgo.Fail(fmt.Sprintf("No context found for worker %d. Make sure SetupWorkerHarness was called in BeforeSuite", workerID))
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

// copyFile copies a file from source to destination, creating the destination directory if needed.
func CopyFile(from, to string) error {
	srcInfo, err := os.Stat(from)
	if err != nil {
		return fmt.Errorf("stat source file %s: %w", from, err)
	}
	if srcInfo.IsDir() {
		return fmt.Errorf("source %s is a directory, not a file", from)
	}

	if err := os.MkdirAll(filepath.Dir(to), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	r, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer r.Close()

	w, err := os.Create(to)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer w.Close()

	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("copying file content: %w", err)
	}

	// Preserve file permissions
	if err := os.Chmod(to, srcInfo.Mode()); err != nil {
		return fmt.Errorf("setting destination file permissions: %w", err)
	}

	return nil
}
