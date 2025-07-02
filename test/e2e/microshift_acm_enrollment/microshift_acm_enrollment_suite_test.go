package microshift

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

func TestMicroshift(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "microshift ACM enrollment E2E Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("microshift ACM enrollment E2E Suite")
})
