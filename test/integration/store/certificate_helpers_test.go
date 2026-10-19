package store_test

import (
	"context"
	"crypto"

	"github.com/flightctl/flightctl/internal/config"
	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type certificatePersistenceSuite struct {
	Ctx      context.Context
	DB       *gorm.DB
	OrgID    uuid.UUID
	Log      *logrus.Logger
	caClient *icrypto.CAClient
	cfg      *config.Config
	dbName   string
}

func newCertificatePersistenceSuite() *certificatePersistenceSuite {
	suite := &certificatePersistenceSuite{Ctx: testutil.StartSpecTracerForGinkgo(suiteCtx), OrgID: uuid.New(), Log: flightlog.InitLogs()}
	var err error
	suite.cfg, suite.dbName, suite.DB, err = testdb.CreateTestDB(suite.Ctx, suite.Log, "csr_", store.InitDB)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(testdb.DeleteTestDB(suite.Ctx, suite.Log, suite.cfg, suite.DB, suite.dbName)).To(Succeed())
	})
	Expect(suite.DB.Create(&model.Organization{ID: suite.OrgID, ExternalID: suite.OrgID.String(), DisplayName: "Certificate persistence"}).Error).To(Succeed())
	suite.caClient, _, err = icrypto.EnsureCA(cacfg.NewDefault(GinkgoT().TempDir()))
	Expect(err).NotTo(HaveOccurred())
	return suite
}

func newStoredCSR() domain.CertificateSigningRequest {
	_, privateKey, err := fcrypto.NewKeyPair()
	Expect(err).NotTo(HaveOccurred())
	name := uuid.NewString()
	request, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), name)
	Expect(err).NotTo(HaveOccurred())
	return domain.CertificateSigningRequest{ApiVersion: "v1beta1", Kind: "CertificateSigningRequest", Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: domain.CertificateSigningRequestSpec{Request: request, SignerName: "flightctl.io/enrollment"}}
}

func seedCSRStatus(provider csrstore.Store, ctx context.Context, orgID uuid.UUID, candidate *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	updated, _, _, err := provider.Mutate(ctx, orgID, *candidate.Metadata.Name, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		mutation.Resource().Status = candidate.Status
		return nil
	})
	return updated, err
}
