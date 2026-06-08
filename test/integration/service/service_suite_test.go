package service_test

import (
	"context"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

var (
	suiteCtx      context.Context
	redisHost     string
	redisPort     uint
	redisPassword domain.SecureString
	redisCleanup  func()
)

func TestServiceSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Service Integration Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Service Integration Suite")
	Expect(integrationstack.EnsureRunning(suiteCtx)).To(Succeed())

	var err error
	redisHost, redisPort, redisPassword, redisCleanup, err = testdb.CreateTestRedis(
		suiteCtx, flightlog.InitLogs())
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	if redisCleanup != nil {
		redisCleanup()
	}
})

// ServiceTestSuite provides common setup and teardown for service integration tests
type ServiceTestSuite struct {
	// Public surface consumed by specs
	Log     *logrus.Logger
	Ctx     context.Context
	Store   store.Store
	Handler service.Service
	OrgID   uuid.UUID

	// VulnerabilityEnabled controls whether vulnerability endpoints are enabled
	// for this suite. Set to true in individual specs before calling Setup().
	VulnerabilityEnabled bool

	// Implementation details
	cfg               *config.Config
	dbName            string
	DB                *gorm.DB
	ctrl              *gomock.Controller
	mockQueueProducer *queues.MockQueueProducer
	workerClient      worker_client.WorkerClient
	caClient          *icrypto.CAClient
}

// Setup performs common initialization for service tests
func (s *ServiceTestSuite) Setup() {
	s.Ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
	s.Log = testutil.InitLogsWithDebug()

	var err error
	s.cfg, s.dbName, s.DB, err = testdb.CreateTestDB(s.Ctx, s.Log, "", store.InitDB)
	Expect(err).NotTo(HaveOccurred())
	s.Store = store.NewStore(s.DB, s.Log.WithField("pkg", "store"))

	// Add a default admin mapped identity to the context for tests
	// This is required by auth provider validation
	testOrg := &model.Organization{
		ID:          store.NullOrgId,
		ExternalID:  "test-org",
		DisplayName: "Test Organization",
	}
	adminIdentity := &identity.MappedIdentity{
		Username:      "test-admin",
		UID:           uuid.New().String(),
		Organizations: []*model.Organization{testOrg},
		OrgRoles:      map[string][]string{"*": {string(api.RoleAdmin)}},
		SuperAdmin:    true, // Super admin required for service tests
	}
	s.Ctx = context.WithValue(s.Ctx, consts.MappedIdentityCtxKey, adminIdentity)

	s.ctrl = gomock.NewController(GinkgoT())
	s.mockQueueProducer = queues.NewMockQueueProducer(s.ctrl)
	s.workerClient = worker_client.NewWorkerClient(s.mockQueueProducer, s.Log)
	s.mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	kvStore, err := kvstore.NewKVStore(s.Ctx, s.Log, redisHost, redisPort, redisPassword)
	Expect(err).ToNot(HaveOccurred())

	// Setup CA for CSR tests
	testDirPath := GinkgoT().TempDir()
	caCfg := ca.NewDefault(testDirPath)
	s.caClient, _, err = icrypto.EnsureCA(caCfg)
	Expect(err).ToNot(HaveOccurred())

	s.Handler = service.NewServiceHandler(s.Store, s.workerClient, kvStore, s.caClient, s.Log, "", "", []string{}, s.VulnerabilityEnabled)
	// Default org for integration tests
	s.OrgID = store.NullOrgId
}

// Teardown performs common cleanup for service tests
func (s *ServiceTestSuite) Teardown() {
	_ = s.Store.Close()
	Expect(testdb.DeleteTestDB(s.Ctx, s.Log, s.cfg, s.DB, s.dbName)).To(Succeed())
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
	result := s.DB.WithContext(s.Ctx).Model(&model.DeviceTimestamp{}).Where("org_id = ? AND name = ?", orgId, deviceName).Updates(map[string]interface{}{
		"last_seen": lastSeen,
	})
	return result.Error
}
