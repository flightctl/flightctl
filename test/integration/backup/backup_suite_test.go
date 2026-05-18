package backup_test

import (
	"context"
	"testing"

	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var suiteCtx context.Context

func TestBackup(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backup Integration Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Backup Integration Suite")
	return nil
}, func(_ []byte) {})
