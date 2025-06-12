package field_selectors

import (
	"context"
	"testing"

	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	suiteCtx context.Context
	ctx      context.Context
)

func TestFieldSelector(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Field selectors Extension E2E Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Field Selectors Extension E2E Suite")
})

const (
	templateImage    = "quay.io/redhat/rhde:9.2"
	repositoryUrl    = "https://github.com/flightctl/flightctl.git"
	devicePrefix     = "device-"
	fleetPrefix      = "fleet-"
	repositoryPrefix = "repository-"
	fleetName        = "fleet-1"
	resourceCount    = 10
)
