package restore_test

import (
	"context"
	"crypto"
	"encoding/base32"
	"strings"
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
	"github.com/flightctl/flightctl/internal/restore"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
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

func TestRestore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Restore Integration Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Restore Integration Suite")
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

// RestoreTestSuite provides common setup and teardown for restore integration tests.
type RestoreTestSuite struct {
	Log          *logrus.Logger
	Ctx          context.Context
	DB           *gorm.DB
	RestoreStore *restore.RestoreStore
	OrgID        uuid.UUID

	// Focused stores/services consumed directly by specs. Only the resources
	// actually exercised by this suite's consumer test files are exposed here.
	DeviceStore            devicestore.Store
	EventStore             eventstore.Store
	EnrollmentRequestStore enrollmentrequeststore.Store

	Device            deviceservice.Service
	Event             eventservice.Service
	EnrollmentRequest enrollmentrequestservice.Service

	cfg    *config.Config
	dbName string
	ctrl   *gomock.Controller
}

// Setup performs common initialization for restore tests.
func (s *RestoreTestSuite) Setup() {
	s.Ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
	s.Log = testutil.InitLogsWithDebug()

	var err error
	s.cfg, s.dbName, s.DB, err = testdb.CreateTestDB(s.Ctx, s.Log, "", store.InitDB)
	Expect(err).NotTo(HaveOccurred())
	s.RestoreStore = restore.NewRestoreStore(s.DB)
	s.OrgID = store.NullOrgId

	s.DeviceStore = devicestore.NewDeviceStore(s.DB, s.Log.WithField("pkg", "device-store"))
	s.EventStore = eventstore.NewEventStore(s.DB, s.Log.WithField("pkg", "event-store"))
	s.EnrollmentRequestStore = enrollmentrequeststore.NewEnrollmentRequestStore(s.DB, s.Log.WithField("pkg", "enrollmentrequest-store"))
	fleetStore := fleetstore.NewFleetStore(s.DB, s.Log.WithField("pkg", "fleet-store"))
	csrStore := certificatesigningrequeststore.NewCertificateSigningRequestStore(s.DB, s.Log.WithField("pkg", "csr-store"))

	// Add a default admin mapped identity to the context
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
		SuperAdmin:    true,
	}
	s.Ctx = context.WithValue(s.Ctx, consts.MappedIdentityCtxKey, adminIdentity)

	s.ctrl = gomock.NewController(GinkgoT())
	mockQueueProducer := queues.NewMockQueueProducer(s.ctrl)
	workerClient := worker_client.NewWorkerClient(mockQueueProducer, s.Log)
	mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	kvStore, err := kvstore.NewKVStore(s.Ctx, s.Log, redisHost, redisPort, redisPassword)
	Expect(err).ToNot(HaveOccurred())

	testDirPath := GinkgoT().TempDir()
	caCfg := ca.NewDefault(testDirPath)
	caClient, _, err := icrypto.EnsureCA(caCfg)
	Expect(err).ToNot(HaveOccurred())

	eventsSvc := events.NewServiceHandler(s.EventStore, workerClient, s.Log)
	s.Device = deviceservice.NewDeviceServiceHandler(s.DeviceStore, fleetStore, eventsSvc, kvStore, "", s.Log)
	s.Event = eventservice.NewServiceHandler(s.EventStore, eventsSvc)
	s.EnrollmentRequest = enrollmentrequestservice.NewServiceHandler(s.EnrollmentRequestStore, s.DeviceStore, csrStore, caClient, kvStore, eventsSvc, s.Log, []string{}, "", "")
}

// Teardown performs common cleanup for restore tests.
func (s *RestoreTestSuite) Teardown() {
	Expect(testdb.DeleteTestDB(s.Ctx, s.Log, s.cfg, s.DB, s.dbName)).To(Succeed())
	if s.ctrl != nil {
		s.ctrl.Finish()
	}
}

// SetDeviceLastSeen sets the lastSeen timestamp for a device directly in the database.
func (s *RestoreTestSuite) SetDeviceLastSeen(deviceName string, lastSeen time.Time) {
	Expect(s.DB.Model(&model.DeviceTimestamp{}).Where("org_id = ? AND name = ?", s.OrgID, deviceName).
		Update("last_seen", lastSeen).Error).ToNot(HaveOccurred())
}

// GenerateDeviceNameAndCSR generates a device name (deterministic) together with a matching PEM-encoded CSR.
func GenerateDeviceNameAndCSR() (string, []byte) {
	publicKey, privateKey, err := fcrypto.NewKeyPair()
	Expect(err).ToNot(HaveOccurred())

	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	Expect(err).ToNot(HaveOccurred())

	deviceName := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash))

	csrPEM, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), deviceName)
	Expect(err).ToNot(HaveOccurred())

	return deviceName, csrPEM
}

// CreateTestER creates a test enrollment request with a valid CSR.
func CreateTestER() api.EnrollmentRequest {
	name, csrData := GenerateDeviceNameAndCSR()
	return api.EnrollmentRequest{
		ApiVersion: "v1beta1",
		Kind:       "EnrollmentRequest",
		Metadata: api.ObjectMeta{
			Name: &name,
			Labels: &map[string]string{
				"test": "integration",
			},
		},
		Spec: api.EnrollmentRequestSpec{
			Csr: string(csrData),
		},
	}
}
