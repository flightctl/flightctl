package flightctl_shutdown

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

func TestFlightCtlShutdown(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "FlightCtl Shutdown Integration Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("FlightCtl Shutdown Integration Suite")
})
