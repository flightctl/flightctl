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
	authproviderservice "github.com/flightctl/flightctl/internal/service/authprovider"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	certificatesigningrequestservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/flightctl/flightctl/internal/store"
	authproviderstore "github.com/flightctl/flightctl/internal/store/authprovider"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
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
	Log *logrus.Logger
	Ctx context.Context

	// Focused stores/services consumed directly by specs. Only the resources
	// actually exercised by this suite's consumer test files are exposed here.
	AuthProviderStore authproviderstore.Store
	EventStore        eventstore.Store
	DeviceStore       devicestore.Store
	OrganizationStore organizationstore.Store

	AuthProvider              authproviderservice.Service
	Catalog                   catalogservice.Service
	CertificateSigningRequest certificatesigningrequestservice.Service
	Device                    deviceservice.Service
	EnrollmentRequest         enrollmentrequestservice.Service
	Fleet                     fleetservice.Service
	Repository                repositoryservice.Service

	OrgID uuid.UUID

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

	s.AuthProviderStore = authproviderstore.NewAuthProviderStore(s.DB, s.Log.WithField("pkg", "authprovider-store"))
	s.EventStore = eventstore.NewEventStore(s.DB, s.Log.WithField("pkg", "event-store"))
	s.DeviceStore = devicestore.NewDeviceStore(s.DB, s.Log.WithField("pkg", "device-store"))
	s.OrganizationStore = organizationstore.NewOrganizationStore(s.DB)
	catalogStore := catalogstore.NewCatalogStore(s.DB, s.Log.WithField("pkg", "catalog-store"))
	csrStore := certificatesigningrequeststore.NewCertificateSigningRequestStore(s.DB, s.Log.WithField("pkg", "csr-store"))
	fleetStore := fleetstore.NewFleetStore(s.DB, s.Log.WithField("pkg", "fleet-store"))
	enrollmentRequestStore := enrollmentrequeststore.NewEnrollmentRequestStore(s.DB, s.Log.WithField("pkg", "enrollmentrequest-store"))
	repositoryStore := repositorystore.NewRepositoryStore(s.DB, s.Log.WithField("pkg", "repository-store"))

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

	eventsSvc := events.NewServiceHandler(s.EventStore, s.workerClient, s.Log)
	s.AuthProvider = authproviderservice.NewServiceHandler(s.AuthProviderStore, eventsSvc, s.Log)
	s.Catalog = catalogservice.NewServiceHandler(catalogStore, eventsSvc, s.Log)
	s.CertificateSigningRequest = certificatesigningrequestservice.NewServiceHandler(csrStore, enrollmentRequestStore, s.caClient, eventsSvc, s.Log, "", "")
	s.Device = deviceservice.NewDeviceServiceHandler(s.DeviceStore, fleetStore, eventsSvc, kvStore, "", s.Log)
	s.EnrollmentRequest = enrollmentrequestservice.NewServiceHandler(enrollmentRequestStore, s.Device, csrStore, s.caClient, kvStore, eventsSvc, s.Log, []string{}, "", "")
	s.Fleet = fleetservice.NewServiceHandler(fleetStore, eventsSvc, s.Log)
	s.Repository = repositoryservice.NewServiceHandler(repositoryStore, eventsSvc, s.Log)

	// Default org for integration tests
	s.OrgID = store.NullOrgId
}

// Teardown performs common cleanup for service tests
func (s *ServiceTestSuite) Teardown() {
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
