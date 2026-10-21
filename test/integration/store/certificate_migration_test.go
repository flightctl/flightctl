package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/migration"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/test/util/testdb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

type legacyCertificateSigningRequest struct {
	model.Resource
	Spec   *model.JSONField[domain.CertificateSigningRequestSpec]   `gorm:"type:jsonb"`
	Status *model.JSONField[domain.CertificateSigningRequestStatus] `gorm:"type:jsonb"`
}

func (legacyCertificateSigningRequest) TableName() string { return "certificate_signing_requests" }

var _ = Describe("Normal retained certificate migration", func() {
	var suite *certificatePersistenceSuite
	BeforeEach(func() { suite = newCertificatePersistenceSuite() })

	It("When migrating an old schema it should derive exact leaf keys and retain duplicate ambiguity", func() {
		cfg, name, database, err := testdb.CreateEmptyTestDB(suite.Ctx, suite.Log, "legacy_csr_", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(testdb.DeleteTestDB(suite.Ctx, suite.Log, cfg, database, name)).To(Succeed()) })
		Expect(database.AutoMigrate(&model.Organization{}, &legacyCertificateSigningRequest{})).To(Succeed())
		Expect(database.Migrator().HasColumn(&legacyCertificateSigningRequest{}, "certificate_digest")).To(BeFalse())
		Expect(database.Create(&model.Organization{ID: suite.OrgID, ExternalID: "legacy-digest", DisplayName: "Legacy"}).Error).To(Succeed())
		request := newStoredCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		alternate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Comment": "old encoding"}, Bytes: leaf.Raw})
		for resourceName, retained := range map[string][]byte{"first": certificate, "second": alternate, "malformed": []byte("not PEM"), "unissued": nil} {
			status := domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}}
			if retained != nil {
				status.Certificate = &retained
			}
			Expect(database.Create(&legacyCertificateSigningRequest{Resource: model.Resource{OrgID: suite.OrgID, Name: resourceName},
				Spec: model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: "historical.example/changed"}), Status: model.MakeJSONField(status)}).Error).To(Succeed())
		}
		padding := make([]legacyCertificateSigningRequest, 505)
		for index := range padding {
			padding[index] = legacyCertificateSigningRequest{Resource: model.Resource{OrgID: suite.OrgID, Name: fmt.Sprintf("padding-%04d", index)},
				Spec: model.MakeJSONField(domain.CertificateSigningRequestSpec{})}
		}
		Expect(database.Create(&padding).Error).To(Succeed())
		Expect(migration.Run(suite.Ctx, database, suite.Log, true)).To(MatchError(migration.ErrDryRunComplete))
		Expect(database.Migrator().HasColumn(&legacyCertificateSigningRequest{}, "certificate_digest")).To(BeFalse())
		Expect(migration.Run(suite.Ctx, database, suite.Log, false)).To(Succeed())
		Expect(database.Migrator().HasIndex(&model.CertificateSigningRequest{}, "idx_csr_org_certificate_digest")).To(BeTrue())
		var definition string
		Expect(database.Raw("SELECT indexdef FROM pg_indexes WHERE tablename = 'certificate_signing_requests' AND indexname = 'idx_csr_org_certificate_digest'").Scan(&definition).Error).To(Succeed())
		Expect(definition).To(ContainSubstring("(org_id, certificate_digest)"))
		Expect(definition).NotTo(ContainSubstring("UNIQUE"))
		digest := sha256.Sum256(leaf.Raw)
		filter := selector.NewFieldSelectorOrDie("internal.certificateDigest="+hex.EncodeToString(digest[:]), selector.WithPrivateSelectors())
		provider := csrstore.NewCertificateSigningRequestStore(database, suite.Log)
		listed, err := provider.List(suite.Ctx, suite.OrgID, store.ListParams{FieldSelector: filter})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed.Items).To(HaveLen(2))
		Expect(migration.Run(suite.Ctx, database, suite.Log, false)).To(Succeed())
		_, err = provider.Delete(suite.Ctx, suite.OrgID, "second")
		Expect(err).NotTo(HaveOccurred())
		listed, err = provider.List(suite.Ctx, suite.OrgID, store.ListParams{FieldSelector: filter})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed.Items).To(HaveLen(1))
		Expect(listed.Items[0].Metadata.Name).To(Equal(lo.ToPtr("first")))
	})

	It("When old certificate encoding is invalid it should roll back migration and permit a corrected retry", func() {
		cfg, name, database, err := testdb.CreateEmptyTestDB(suite.Ctx, suite.Log, "invalid_csr_", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(testdb.DeleteTestDB(suite.Ctx, suite.Log, cfg, database, name)).To(Succeed()) })
		Expect(database.AutoMigrate(&model.Organization{}, &legacyCertificateSigningRequest{})).To(Succeed())
		Expect(database.Create(&model.Organization{ID: suite.OrgID, ExternalID: "invalid-digest", DisplayName: "Invalid"}).Error).To(Succeed())
		Expect(database.Exec(`INSERT INTO certificate_signing_requests (org_id, name, spec, status) VALUES (?, 'invalid', '{}', '{"certificate":"!bad-base64!"}')`, suite.OrgID).Error).To(Succeed())
		Expect(migration.Run(suite.Ctx, database, suite.Log, false)).To(MatchError(ContainSubstring("retained certificate encoding")))
		Expect(database.Migrator().HasColumn(&legacyCertificateSigningRequest{}, "certificate_digest")).To(BeFalse())
		Expect(database.Exec(`UPDATE certificate_signing_requests SET status = '{"certificate":null}' WHERE name = 'invalid'`).Error).To(Succeed())
		Expect(migration.Run(suite.Ctx, database, suite.Log, false)).To(Succeed())
	})

})
