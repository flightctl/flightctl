package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/migration"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
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

var _ = Describe("Indexed retained enrollment leaves", func() {
	var suite *ServiceTestSuite
	BeforeEach(func() { suite = NewServiceTestSuite(); suite.Setup() })
	AfterEach(func() { suite.Teardown() })

	It("When migrating an old schema it should derive exact leaf keys and retain duplicate ambiguity", func() {
		cfg, name, database, err := testdb.CreateEmptyTestDB(suite.Ctx, suite.Log, "legacy_csr_", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(testdb.DeleteTestDB(suite.Ctx, suite.Log, cfg, database, name)).To(Succeed()) })
		Expect(database.AutoMigrate(&model.Organization{}, &legacyCertificateSigningRequest{})).To(Succeed())
		Expect(database.Migrator().HasColumn(&legacyCertificateSigningRequest{}, "certificate_digest")).To(BeFalse())
		Expect(database.Create(&model.Organization{ID: suite.OrgID, ExternalID: "legacy-digest", DisplayName: "Legacy"}).Error).To(Succeed())
		request := CreateTestCSR()
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
		provider := csrstore.NewCertificateSigningRequestStore(database, suite.Log, "example.org/enrollment")
		listed, err := provider.List(suite.Ctx, suite.OrgID, store.ListParams{FieldSelector: filter})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed.Items).To(HaveLen(2))
		resolved, err := provider.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(BeNil())
		Expect(migration.Run(suite.Ctx, database, suite.Log, false)).To(Succeed())
		_, err = provider.Delete(suite.Ctx, suite.OrgID, "second")
		Expect(err).NotTo(HaveOccurred())
		resolved, err = provider.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(lo.ToPtr("first")))
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

	It("When status changes it should index the committed first enrollment leaf and mutable non-enrollment replacements", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, "example.org/enrollment")
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		first, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		second, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Raw).NotTo(Equal(first.Raw))
		for _, enrollment := range []bool{true, false} {
			resource := request
			resource.Spec.SignerName = "example.org/management"
			if enrollment {
				resource.Spec.SignerName = "example.org/enrollment"
			}
			resource.Metadata.Name = lo.ToPtr(uuid.NewString())
			created, err := provider.Create(suite.Ctx, suite.OrgID, &resource)
			Expect(err).NotTo(HaveOccurred())
			created.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: first.Raw}))
			published, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, created)
			Expect(err).NotTo(HaveOccurred())
			published.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: second.Raw}))
			updated, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, published)
			Expect(err).NotTo(HaveOccurred())
			Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *updated.Metadata.Name, []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}})).To(Succeed())
			retained := second.Raw
			if enrollment {
				retained = first.Raw
			}
			resolved, err := provider.FindNameByCertificate(suite.Ctx, suite.OrgID, retained)
			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).To(Equal(updated.Metadata.Name))
			if !enrollment {
				current, err := provider.Get(suite.Ctx, suite.OrgID, *updated.Metadata.Name)
				Expect(err).NotTo(HaveOccurred())
				current.Status.Certificate = nil
				_, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, current)
				Expect(err).NotTo(HaveOccurred())
				resolved, err = provider.FindNameByCertificate(suite.Ctx, suite.OrgID, retained)
				Expect(err).NotTo(HaveOccurred())
				Expect(resolved).To(BeNil())
			}
			_, err = provider.Delete(suite.Ctx, suite.OrgID, *updated.Metadata.Name)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("When a digest candidate has a different leaf it should not create false uniqueness or cross tenants", func() {
		request := CreateTestCSR()
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(util.WithOrganizationID(suite.Ctx, suite.OrgID), suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*created.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		other := CreateTestCSR()
		other.Spec.SignerName = "example.org/management"
		candidate, err := provider.Create(suite.Ctx, suite.OrgID, &other)
		Expect(err).NotTo(HaveOccurred())
		parsed, err := fcrypto.ParseCSR(other.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		different, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600)
		Expect(err).NotTo(HaveOccurred())
		Expect(different.Raw).NotTo(Equal(leaf.Raw))
		candidate.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: different.Raw}))
		_, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, candidate)
		Expect(err).NotTo(HaveOccurred())
		digest := sha256.Sum256(leaf.Raw)
		Expect(suite.DB.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *candidate.Metadata.Name).Update("certificate_digest", hex.EncodeToString(digest[:])).Error).To(Succeed())
		resolved, err := provider.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(created.Metadata.Name))
		resolved, err = provider.FindNameByCertificate(suite.Ctx, uuid.New(), leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(BeNil())
		_, err = provider.List(suite.Ctx, suite.OrgID, store.ListParams{FieldSelector: selector.NewFieldSelectorOrDie("internal.certificateDigest=" + hex.EncodeToString(digest[:]))})
		Expect(err).To(HaveOccurred())
		cancelled, cancel := context.WithCancel(suite.Ctx)
		cancel()
		_, err = provider.FindNameByCertificate(cancelled, suite.OrgID, leaf.Raw)
		Expect(err).To(HaveOccurred())
		Expect(suite.DB.Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, *candidate.Metadata.Name).
			Update("status", model.MakeJSONField(map[string]any{"certificate": "!bad-base64!"})).Error).To(Succeed())
		resolved, err = provider.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).To(HaveOccurred())
		Expect(resolved).To(BeNil())
	})

	DescribeTable("When a retained enrollment signer outlives a spec change it should preserve its key and known or unknown issuance time", func(knownTime bool) {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, "example.org/enrollment")
		request := CreateTestCSR()
		request.Spec.SignerName = "example.org/enrollment"
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600, signer.WithExtension(signer.OIDSignerName, "example.org/enrollment"))
		Expect(err).NotTo(HaveOccurred())
		created, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		created.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
		var expectedTime *time.Time
		if knownTime {
			expectedTime = lo.ToPtr(time.Date(2025, 3, 4, 5, 6, 7, 123456000, time.UTC))
		}
		created.Status.IssuedAt = expectedTime
		published, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		published.Spec.SignerName = "example.org/management"
		updated, _, err := provider.Update(suite.Ctx, suite.OrgID, published)
		Expect(err).NotTo(HaveOccurred())
		updated.Status.Certificate = lo.ToPtr([]byte("losing replacement"))
		updated.Status.IssuedAt = lo.ToPtr(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
		retained, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, updated)
		Expect(err).NotTo(HaveOccurred())
		Expect(retained.Status.IssuedAt).To(Equal(expectedTime))
		Expect(provider.UpdateConditions(suite.Ctx, suite.OrgID, *retained.Metadata.Name, []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}})).To(Succeed())
		resolved, err := provider.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(retained.Metadata.Name))
	}, Entry("known first issuance time", true), Entry("historically unknown issuance time", false))
})
