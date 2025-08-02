package global_setup

import (
	"os"
	"sync"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
)

var (
	globalSetupOnce    sync.Once
	globalTeardownOnce sync.Once
)

// RunGlobalSetup ensures global setup runs exactly once across all test suites
func RunGlobalSetup(harness *e2e.Harness) {
	globalSetupOnce.Do(func() {
		GinkgoWriter.Printf("🔄 [Global E2E Setup] Running global initialization for all E2E tests...\n")
		harness.CleanUpAllResources()
		GinkgoWriter.Printf("✅ [Global E2E Setup] Global initialization completed\n")
	})
}

// RunGlobalTeardown ensures global teardown runs exactly once across all test suites
func RunGlobalTeardown() {
	globalTeardownOnce.Do(func() {
		GinkgoWriter.Printf("🔄 [Global E2E Teardown] Running global cleanup...\n")
		GinkgoWriter.Printf("✅ [Global E2E Teardown] Global cleanup completed\n")
	})
}

// TestMain runs once when this package is imported
func TestMain(m *testing.M) {
	// This won't actually run since this package doesn't have tests,
	// but it's here for completeness
	os.Exit(m.Run())
}
