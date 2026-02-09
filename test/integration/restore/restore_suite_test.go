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
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/restore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

var suiteCtx context.Context

func TestRestore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Restore Integration Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Restore Integration Suite")
	return nil
}, func(_ []byte) {})

// RestoreTestSuite provides common setup and teardown for restore integration tests.
type RestoreTestSuite struct {
	Log          *logrus.Logger
	Ctx          context.Context
	DB           *gorm.DB
	Store        store.Store
	RestoreStore *restore.RestoreStore
	Handler      service.Service
	OrgID        uuid.UUID

	cfg    *config.Config
	dbName string
	ctrl   *gomock.Controller
}

// Setup performs common initialization for restore tests.
func (s *RestoreTestSuite) Setup() {
	s.Ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
	s.Log = testutil.InitLogsWithDebug()

	s.Store, s.cfg, s.dbName, s.DB = store.PrepareDBForUnitTests(s.Ctx, s.Log)
	s.RestoreStore = restore.NewRestoreStore(s.DB)
	s.OrgID = store.NullOrgId

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

	kvStore, err := kvstore.NewKVStore(s.Ctx, s.Log, "localhost", 6379, "adminpass")
	Expect(err).ToNot(HaveOccurred())

	testDirPath := GinkgoT().TempDir()
	caCfg := ca.NewDefault(testDirPath)
	caClient, _, err := icrypto.EnsureCA(caCfg)
	Expect(err).ToNot(HaveOccurred())

	s.Handler = service.NewServiceHandler(s.Store, workerClient, kvStore, caClient, s.Log, "", "", []string{})
}

// Teardown performs common cleanup for restore tests.
func (s *RestoreTestSuite) Teardown() {
	store.DeleteTestDB(s.Ctx, s.Log, s.cfg, s.Store, s.dbName)
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
