package tpm

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTPM(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TPM E2E Suite")
}

const (
	TIMEOUT      = time.Minute
	LONG_TIMEOUT = 10 * time.Minute
	POLLING      = time.Second
	LONG_POLLING = 10 * time.Second
)

const realTPMDevice = "/dev/tpm0"

var hasRealTPM bool

func init() {
	SetDefaultEventuallyTimeout(TIMEOUT)
	SetDefaultEventuallyPollingInterval(POLLING)
}

var _ = BeforeSuite(func() {
	auxiliary.Get(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	e2e.SetupWorkerHarnessOrAbort()

	suiteCtx := e2e.GetWorkerContext()

	hasRealTPM = e2e.HostHasTPMDevice(realTPMDevice)

	err := InjectTPMCerts(suiteCtx, hasRealTPM)
	Expect(err).ToNot(HaveOccurred())
})
