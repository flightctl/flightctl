package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

var suiteCtx context.Context

func TestServiceSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Service Integration Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	// Initialize the root tracer/span for the entire service integration suite once
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Service Integration Suite")
	return nil
}, func(_ []byte) {})

// ServiceTestSuite provides common setup and teardown for service integration tests
type ServiceTestSuite struct {
	// Public surface consumed by specs
	Log     *logrus.Logger
	Ctx     context.Context
	Store   store.Store
	Handler service.Service

	// Private implementation details – not needed by tests
	cfg               *config.Config
	dbName            string
	db                *gorm.DB
	ctrl              *gomock.Controller
	mockQueueProducer *queues.MockQueueProducer
	workerClient      worker_client.WorkerClient
	caClient          *icrypto.CAClient
}

// Setup performs common initialization for service tests
func (s *ServiceTestSuite) Setup() {
	s.Ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
	s.Log = flightlog.InitLogs()

	s.Store, s.cfg, s.dbName, s.db = store.PrepareDBForUnitTests(s.Ctx, s.Log)

	s.ctrl = gomock.NewController(GinkgoT())
	s.mockQueueProducer = queues.NewMockQueueProducer(s.ctrl)
	s.workerClient = worker_client.NewWorkerClient(s.mockQueueProducer, s.Log)
	s.mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	kvStore, err := kvstore.NewKVStore(s.Ctx, s.Log, "localhost", 6379, "adminpass")
	Expect(err).ToNot(HaveOccurred())

	// Setup CA for CSR tests
	testDirPath := GinkgoT().TempDir()
	caCfg := ca.NewDefault(testDirPath)
	s.caClient, _, err = icrypto.EnsureCA(caCfg)
	Expect(err).ToNot(HaveOccurred())

	orgResolver, err := testutil.NewOrgResolver(s.cfg, s.Store.Organization(), s.Log)
	Expect(err).ToNot(HaveOccurred())
	s.Handler = service.NewServiceHandler(s.Store, s.workerClient, kvStore, s.caClient, s.Log, "", "", []string{}, orgResolver)
}

// Teardown performs common cleanup for service tests
func (s *ServiceTestSuite) Teardown() {
	store.DeleteTestDB(s.Ctx, s.Log, s.cfg, s.Store, s.dbName)
	if s.ctrl != nil {
		s.ctrl.Finish()
	}
}

// NewServiceTestSuite creates a new test suite instance
func NewServiceTestSuite() *ServiceTestSuite {
	return &ServiceTestSuite{}
}

// SetDeviceLastSeen sets the lastSeen timestamp for a device directly in the database
func (s *ServiceTestSuite) SetDeviceLastSeen(deviceName string, lastSeen time.Time) error {
	orgId := store.NullOrgId
	result := s.db.WithContext(s.Ctx).Model(&model.DeviceTimestamp{}).Where("org_id = ? AND name = ?", orgId, deviceName).Updates(map[string]interface{}{
		"last_seen": lastSeen,
	})
	return result.Error
}
