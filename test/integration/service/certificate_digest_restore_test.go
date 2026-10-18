package service_test

import (
	"context"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/restore"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type digestRestoreDeployment struct {
	cfg      *config.Config
	started  bool
	database *gorm.DB
	log      *logrus.Logger
	orgID    uuid.UUID
	leafDER  []byte
}

func (deployment *digestRestoreDeployment) Type() backup.DeploymentType {
	return backup.DeploymentTypePodman
}
func (deployment *digestRestoreDeployment) StopServices(context.Context) error {
	deployment.started = false
	return nil
}
func (deployment *digestRestoreDeployment) RestoreDatabase(context.Context, string) error { return nil }
func (deployment *digestRestoreDeployment) RestorePKI(context.Context, string) error      { return nil }
func (deployment *digestRestoreDeployment) RestoreEncryptionKeys(context.Context, string) error {
	return nil
}
func (deployment *digestRestoreDeployment) RestoreConfig(context.Context, string) error { return nil }
func (deployment *digestRestoreDeployment) StartServices(ctx context.Context) error {
	provider := csrstore.NewCertificateSigningRequestStore(deployment.database, deployment.log, "example.org/enrollment")
	resolved, err := provider.FindNameByCertificate(ctx, deployment.orgID, deployment.leafDER)
	if err != nil {
		return err
	}
	if resolved == nil || *resolved != "restored" {
		return fmt.Errorf("restored leaf unavailable before starting consumers")
	}
	deployment.started = true
	return nil
}
func (deployment *digestRestoreDeployment) GetConfig(context.Context) (*config.Config, error) {
	return deployment.cfg, nil
}
func (deployment *digestRestoreDeployment) SetupExternalDBCerts(context.Context, *config.Config) (func(), error) {
	return nil, nil
}
func (deployment *digestRestoreDeployment) ExposeService(_ context.Context, name string) (string, int, func(), error) {
	if name == "flightctl-kv" {
		return redisHost, int(redisPort), func() {}, nil
	}
	return "", 0, nil, fmt.Errorf("unexpected service %s", name)
}

var _ = Describe("Restored retained certificate lookup", func() {
	var suite *ServiceTestSuite
	BeforeEach(func() { suite = NewServiceTestSuite(); suite.Setup() })
	AfterEach(func() { suite.Teardown() })
	DescribeTable("When restoring an old database it should migrate before starting consumers", func(invalid bool) {
		cfg, name, database, err := testdb.CreateEmptyTestDB(suite.Ctx, suite.Log, "restored_csr_", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(testdb.DeleteTestDB(suite.Ctx, suite.Log, cfg, database, name)).To(Succeed()) })
		Expect(database.AutoMigrate(&model.Organization{}, &legacyCertificateSigningRequest{})).To(Succeed())
		Expect(database.Migrator().HasColumn(&legacyCertificateSigningRequest{}, "certificate_digest")).To(BeFalse())
		Expect(database.Create(&model.Organization{ID: suite.OrgID, ExternalID: "restored", DisplayName: "Restored"}).Error).To(Succeed())
		parsed, err := fcrypto.ParseCSR(CreateTestCSR().Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		Expect(database.Create(&legacyCertificateSigningRequest{Resource: model.Resource{OrgID: suite.OrgID, Name: "restored"},
			Spec:   model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: "historical.example/changed"}),
			Status: model.MakeJSONField(domain.CertificateSigningRequestStatus{Certificate: &certificate})}).Error).To(Succeed())
		if invalid {
			Expect(database.Exec(`UPDATE certificate_signing_requests SET status = '{"certificate":"!bad-base64!"}'`).Error).To(Succeed())
		}
		cfg.Database.MigrationUser = cfg.Database.User
		cfg.Database.MigrationPassword = cfg.Database.Password
		cfg.KV.Password = redisPassword
		deployment := &digestRestoreDeployment{cfg: cfg, database: database, log: suite.Log, orgID: suite.OrgID, leafDER: leaf.Raw}
		archive, _, err := backup.CreateArchive(suite.Ctx, GinkgoT().TempDir(), GinkgoT().TempDir(), backup.BackupMetadata{
			Timestamp: time.Now(), Version: "old-schema", DeploymentType: backup.DeploymentTypePodman, DatabaseIncluded: false,
		}, suite.Log)
		Expect(err).NotTo(HaveOccurred())
		err = restore.Restore(suite.Ctx, archive, deployment, suite.Log)
		if invalid {
			Expect(err).To(MatchError(ContainSubstring("retained certificate encoding")))
			Expect(deployment.started).To(BeFalse())
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(deployment.started).To(BeTrue())
		provider := csrstore.NewCertificateSigningRequestStore(database, suite.Log, "example.org/enrollment")
		resolved, err := provider.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(lo.ToPtr("restored")))
	}, Entry("valid old retained leaf", false), Entry("invalid stored encoding", true))
})
