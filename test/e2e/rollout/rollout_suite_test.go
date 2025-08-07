package rollout_test

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

func TestRollout(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Rollout Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Rollout Suite")
})
