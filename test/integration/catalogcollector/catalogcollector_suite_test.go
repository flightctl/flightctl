package catalogcollector_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	suiteCtx     context.Context
	redisCleanup func()

	// suiteRedis* are the connection parameters of the ephemeral Redis this
	// suite process owns. Each API server the specs start opens its own
	// key-value store against them, exactly as the production server does.
	suiteRedisHost     string
	suiteRedisPort     uint
	suiteRedisPassword domain.SecureString

	// suiteAuthProvider is the identity provider every API server in this
	// suite trusts. The Flight Control API server authenticates every request,
	// so the collector and the harness both present the token it accepts.
	suiteAuthProvider *testAuthProvider
)

func TestCatalogCollectorSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Catalog Collector Integration Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Catalog Collector Integration Suite")
	Expect(integrationstack.EnsureRunning(suiteCtx)).To(Succeed())

	log := flightlog.InitLogs()

	var err error
	suiteRedisHost, suiteRedisPort, suiteRedisPassword, redisCleanup, err =
		testdb.CreateTestRedis(suiteCtx, log)
	Expect(err).NotTo(HaveOccurred())

	suiteAuthProvider = startTestAuthProvider()
})

var _ = AfterSuite(func() {
	if suiteAuthProvider != nil {
		suiteAuthProvider.Close()
	}
	if redisCleanup != nil {
		redisCleanup()
	}
})
