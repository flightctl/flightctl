package cli_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// suiteCtx is shared across all CLI E2E specs so they can attach
// sub-tracers to a single parent span.
var suiteCtx context.Context

var _ = BeforeSuite(func() {
	suiteCtx = util.InitSuiteTracerForGinkgo("CLI E2E Suite")

	// A best-effort clean-up to ensure the cluster is empty before tests start.
	h := e2e.NewTestHarness(suiteCtx)
	fmt.Println("[BeforeSuite] Cleaning existing resources …")
	Expect(h.CleanUpAllResources()).To(Succeed())
})

// TestCLI is the single entry-point that runs the whole spec set.
func TestCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CLI E2E Suite")
}
