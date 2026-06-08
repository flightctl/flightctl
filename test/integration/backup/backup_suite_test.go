package backup_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var suiteCtx context.Context

func TestBackup(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backup Integration Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Backup Integration Suite")
	Expect(integrationstack.EnsureRunning(suiteCtx)).To(Succeed())
})
