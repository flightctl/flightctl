package redis_restart

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRedisRestart(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Redis Restart E2E Suite")
}

const (
	// Eventually polling timeout/interval constants
	TIMEOUT      = 5 * time.Minute
	LONG_TIMEOUT = 10 * time.Minute
	POLLING      = 2 * time.Second
	LONG_POLLING = 5 * time.Second
)

// Initialize suite-specific settings
func init() {
	SetDefaultEventuallyTimeout(TIMEOUT)
	SetDefaultEventuallyPollingInterval(POLLING)
}

var _ = BeforeSuite(func() {
	GinkgoWriter.Printf("🚀 Starting Redis Restart E2E Test Suite\n")
	// Setup VM and harness for this worker
	_, _, err := e2e.SetupWorkerHarness()
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("[AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	// Get the harness and context directly - no shared variables needed
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Clean up test resources BEFORE switching back to suite context
	// This ensures we use the correct test ID for resource cleanup
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	// Now restore suite context for any remaining cleanup operations
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})

var _ = AfterSuite(func() {
	GinkgoWriter.Printf("✅ Redis Restart E2E Test Suite Completed\n")
})
