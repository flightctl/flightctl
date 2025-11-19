package redis_restart

import (
	"testing"
	"time"

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
})

var _ = AfterSuite(func() {
	GinkgoWriter.Printf("✅ Redis Restart E2E Test Suite Completed\n")
})

