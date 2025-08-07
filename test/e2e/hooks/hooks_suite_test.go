package hooks

import (
	"context"
	"testing"

	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	suiteCtx context.Context
)

func TestHooks(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Hooks E2E Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Hooks E2E Suite")
})
