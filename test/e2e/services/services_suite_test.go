package services

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestServices(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "FlightCtl Services E2E Suite")
}

const (
	// Eventually polling timeout/interval constants
	TIMEOUT      = 2 * time.Minute
	LONG_TIMEOUT = 10 * time.Minute
	POLLING      = time.Second
	LONG_POLLING = 5 * time.Second
)

// Initialize suite-specific settings
func init() {
	SetDefaultEventuallyTimeout(TIMEOUT)
	SetDefaultEventuallyPollingInterval(POLLING)
}

var _ = BeforeSuite(func() {
	GinkgoWriter.Printf("🚀 Starting FlightCtl Services E2E Test Suite\n")
	// No VM setup needed - testing host system services
})

var _ = AfterSuite(func() {
	GinkgoWriter.Printf("✅ FlightCtl Services E2E Test Suite Completed\n")
})

