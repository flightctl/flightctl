package service_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/tpmcsr"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	erstore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Enrollment credential identity", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	It("When issuing an enrollment credential it should attest the exact CSR resource name", func() {
		_, privateKey, err := fcrypto.NewKeyPair()
		Expect(err).NotTo(HaveOccurred())
		requestPEM, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), "edge-enrollment")
		Expect(err).NotTo(HaveOccurred())
		request := CreateTestCSR()
		request.Metadata.Name = lo.ToPtr("edge-enrollment")
		request.Spec.Request = requestPEM
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.Certificate).NotTo(BeNil())
		stored, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, "edge-enrollment")
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		leaf, err := fcrypto.ParseCertificatePEM(*stored.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		var identity string
		for _, extension := range leaf.Extensions {
			if extension.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 4}) {
				rest, err := asn1.Unmarshal(extension.Value, &identity)
				Expect(err).NotTo(HaveOccurred())
				Expect(rest).To(BeEmpty())
			}
		}
		Expect(identity).To(Equal("edge-enrollment"))
	})

	It("When a CSR supplies the reserved identity extension it should reject even a matching value", func() {
		_, privateKey, err := fcrypto.NewKeyPair()
		Expect(err).NotTo(HaveOccurred())
		identity, err := asn1.Marshal("edge-enrollment")
		Expect(err).NotTo(HaveOccurred())
		requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
			Subject: pkix.Name{CommonName: "edge-enrollment"},
			ExtraExtensions: []pkix.Extension{{
				Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 4}, Value: identity,
			}},
		}, privateKey)
		Expect(err).NotTo(HaveOccurred())
		request := CreateTestCSR()
		request.Metadata.Name = lo.ToPtr("edge-enrollment")
		request.Spec.Request = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER})
		_, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
	})

	It("When a programmatic signing request supplies reserved ExtraExtensions it should reject a matching identity", func() {
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		identity, err := asn1.Marshal(*request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		parsed.ExtraExtensions = []pkix.Extension{{
			Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 4}, Value: identity,
		}}
		signRequest, err := signer.NewSignRequest("flightctl.io/enrollment", *parsed, signer.WithResourceName(*request.Metadata.Name))
		Expect(err).NotTo(HaveOccurred())
		issued, err := signer.Sign(suite.Ctx, suite.caClient, signRequest)
		Expect(err).To(MatchError(ContainSubstring("issuer-owned")))
		Expect(issued).To(BeNil())
	})

	It("When signing an already admitted request it should preserve signing without reapplying admission checks", func() {
		credential := CreateTestCSR()
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, credential)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		peer, err := fcrypto.ParseCertificatePEM(*created.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		signRequest, err := signer.NewSignRequest("flightctl.io/enrollment", *parsed, signer.WithResourceName(*request.Metadata.Name))
		Expect(err).NotTo(HaveOccurred())
		Expect(signer.Verify(suite.Ctx, suite.caClient, signRequest)).To(Succeed())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, peer)
		Expect(signer.Verify(ctx, suite.caClient, signRequest)).To(MatchError(ContainSubstring("bootstrap certificates cannot be requested")))
		rejected, err := signer.SignVerified(ctx, suite.caClient, signRequest)
		Expect(err).To(MatchError(ContainSubstring("bootstrap certificates cannot be requested")))
		Expect(rejected).To(BeNil())
		issued, err := signer.Sign(ctx, suite.caClient, signRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(issued).NotTo(BeNil())
	})

	It("When the enrollment issuer context differs from the persisted tenant it should reject credential creation", func() {
		ctx := util.WithOrganizationID(suite.Ctx, uuid.MustParse("11111111-1111-4111-8111-111111111111"))
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(ctx, suite.OrgID, CreateTestCSR())
		Expect(status.Code).To(BeEquivalentTo(http.StatusBadRequest))
		Expect(created).To(BeNil())
	})

	It("When no context tenant is supplied it should attest the persisted enrollment CSR tenant", func() {
		tenant := uuid.MustParse("22222222-2222-4222-8222-222222222222")
		_, err := suite.OrganizationStore.Create(suite.Ctx, &model.Organization{
			ID: tenant, ExternalID: "enrollment-issuer-tenant", DisplayName: "Enrollment issuer tenant",
		})
		Expect(err).NotTo(HaveOccurred())
		_, present := util.GetOrgIdFromContext(suite.Ctx)
		Expect(present).To(BeFalse())
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, tenant, CreateTestCSR())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.Certificate).NotTo(BeNil())
		leaf, err := fcrypto.ParseCertificatePEM(*created.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		issuerTenant, present, err := signer.GetOrgIDExtensionFromCert(leaf)
		Expect(err).NotTo(HaveOccurred())
		Expect(present).To(BeTrue())
		Expect(issuerTenant.String()).To(Equal("22222222-2222-4222-8222-222222222222"))
	})

	It("When publishing a credential it should persist server-owned issuance time with its certificate", func() {
		before := time.Now().Add(-time.Second)
		request := CreateTestCSR()
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		stored, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		statusJSON, err := json.Marshal(stored.Status)
		Expect(err).NotTo(HaveOccurred())
		var fields map[string]interface{}
		Expect(json.Unmarshal(statusJSON, &fields)).To(Succeed())
		Expect(fields["issuedAt"]).To(BeAssignableToTypeOf(""))
		issuedAt, err := time.Parse(time.RFC3339Nano, fields["issuedAt"].(string))
		Expect(err).NotTo(HaveOccurred())
		Expect(issuedAt.After(before)).To(BeTrue())
		Expect(issuedAt.After(time.Now())).To(BeFalse())
		Expect(stored.Status.Certificate).To(Equal(created.Status.Certificate))
	})

	It("When credential publication fails it should return no unretained certificate and record the failure", func() {
		persistence := &unavailableIssuancePersistence{
			Store: csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName),
		}
		eventService := events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log)
		service := csrservice.NewServiceHandler(persistence, tpmcsr.NewVerifier(suite.EnrollmentRequest),
			suite.caClient, eventService, suite.Log, "", "")
		request := CreateTestCSR()
		created, status := service.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.Certificate).To(BeNil())
		Expect(created.Status.IssuedAt).To(BeNil())
		Expect(domain.IsStatusConditionTrue(created.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed)).To(BeTrue())
		stored, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(stored.Status.Certificate).To(BeNil())
		Expect(stored.Status.IssuedAt).To(BeNil())
		Expect(domain.IsStatusConditionTrue(stored.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed)).To(BeTrue())
	})

	It("When untrusted CSR documents forge issuance status it should publish and preserve only server-owned evidence", func() {
		request := CreateTestCSR()
		forgedTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		request.Status = &domain.CertificateSigningRequestStatus{
			Certificate: lo.ToPtr([]byte("caller-certificate")), IssuedAt: &forgedTime,
		}
		created, status := csrservice.CreateCertificateSigningRequestFromUntrusted(suite.Ctx, suite.CertificateSigningRequest, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.Certificate).NotTo(BeNil())
		Expect(string(*created.Status.Certificate)).NotTo(Equal("caller-certificate"))
		Expect(created.Status.IssuedAt).NotTo(BeNil())
		Expect(created.Status.IssuedAt.After(forgedTime)).To(BeTrue())
		replaced, status := csrservice.ReplaceCertificateSigningRequestFromUntrusted(suite.Ctx, suite.CertificateSigningRequest, suite.OrgID, *request.Metadata.Name, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(replaced.Status.Certificate).To(Equal(created.Status.Certificate))
		Expect(replaced.Status.IssuedAt).To(Equal(created.Status.IssuedAt))
		encoded, err := json.Marshal(replaced)
		Expect(err).NotTo(HaveOccurred())
		var decoded domain.CertificateSigningRequest
		Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
		Expect(decoded.Status.IssuedAt).To(Equal(created.Status.IssuedAt))
	})

	It("When approval supplies forged issuance evidence it should issue only server-owned evidence", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		request := CreateTestCSR()
		_, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		forgedTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		request.Status = &domain.CertificateSigningRequestStatus{
			Certificate: lo.ToPtr([]byte("caller-certificate")), IssuedAt: &forgedTime,
			Conditions: []domain.Condition{{
				Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue,
				Reason: "Approved", Message: "Approved",
			}},
		}
		approved, status := suite.CertificateSigningRequest.UpdateCertificateSigningRequestApproval(suite.Ctx, suite.OrgID, *request.Metadata.Name, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(approved.Status.Certificate).NotTo(BeNil())
		Expect(string(*approved.Status.Certificate)).NotTo(Equal("caller-certificate"))
		Expect(approved.Status.IssuedAt).NotTo(BeNil())
		Expect(approved.Status.IssuedAt.After(forgedTime)).To(BeTrue())
		stored, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(stored.Status.Certificate).To(Equal(approved.Status.Certificate))
		Expect(stored.Status.IssuedAt).To(Equal(approved.Status.IssuedAt))
	})

	It("When updating published credential status it should retain the first certificate and issuance time", func() {
		request := CreateTestCSR()
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		certificate := append([]byte(nil), (*created.Status.Certificate)...)
		issuedAt := *created.Status.IssuedAt
		created.Status.Certificate = lo.ToPtr([]byte("replacement"))
		created.Status.IssuedAt = lo.ToPtr(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		updated, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, created)
		Expect(err).NotTo(HaveOccurred())
		Expect(*updated.Status.Certificate).To(Equal(certificate))
		Expect(updated.Status.IssuedAt.Equal(issuedAt)).To(BeTrue())
	})

	DescribeTable("When deleting issued CSR names it should reserve only the configured enrollment identity",
		func(signerName string, reserved, changeSigner bool) {
			provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, "example.org/bootstrap")
			request := CreateTestCSR()
			request.Spec.SignerName = signerName
			stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
			Expect(err).NotTo(HaveOccurred())
			parsed, err := fcrypto.ParseCSR(request.Spec.Request)
			Expect(err).NotTo(HaveOccurred())
			leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
				signer.WithExtension(signer.OIDSignerName, signerName))
			Expect(err).NotTo(HaveOccurred())
			stored.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
			published, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, stored)
			Expect(err).NotTo(HaveOccurred())
			Expect(published.Status.IssuedAt).To(BeNil())
			if changeSigner {
				published.Spec.SignerName = "flightctl.io/device-enrollment"
				_, _, err = provider.Update(suite.Ctx, suite.OrgID, published)
				Expect(err).NotTo(HaveOccurred())
			}
			deleted, err := provider.Delete(suite.Ctx, suite.OrgID, *request.Metadata.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(deleted).To(BeTrue())
			_, err = provider.Create(suite.Ctx, suite.OrgID, &request)
			if reserved {
				Expect(err).To(MatchError(flterrors.ErrDuplicateName))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("configured enrollment signer, historical leaf without identity metadata", "example.org/bootstrap", true, false),
		Entry("issued enrollment identity remains reserved after a signer-spec change", "example.org/bootstrap", true, true),
		Entry("default name is not enrollment when configured differently", "flightctl.io/enrollment", false, false),
		Entry("management signer", "flightctl.io/device-enrollment", false, false),
	)

	DescribeTable("When replacing published CSR status it should retain only configured enrollment issuance",
		func(signerName, expectedCertificate string) {
			provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, "example.org/bootstrap")
			request := CreateTestCSR()
			request.Spec.SignerName = signerName
			stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
			Expect(err).NotTo(HaveOccurred())
			stored.Status.Certificate = lo.ToPtr([]byte("first-publication"))
			published, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, stored)
			Expect(err).NotTo(HaveOccurred())
			published.Status.Certificate = lo.ToPtr([]byte("replacement-publication"))
			updated, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, published)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(*updated.Status.Certificate)).To(Equal(expectedCertificate))
			current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(*current.Status.Certificate)).To(Equal(expectedCertificate))
		},
		Entry("configured enrollment signer", "example.org/bootstrap", "first-publication"),
		Entry("default name is not enrollment when configured differently", "flightctl.io/enrollment", "replacement-publication"),
		Entry("management signer", "flightctl.io/device-enrollment", "replacement-publication"),
	)

	It("When a historical credential has no issuance time it should keep that time unknown on later status writes", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		request := CreateTestCSR()
		stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		stored.Status.Certificate = lo.ToPtr([]byte("historical-certificate"))
		published, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, stored)
		Expect(err).NotTo(HaveOccurred())
		Expect(published.Status.IssuedAt).To(BeNil())
		published.Status.Certificate = lo.ToPtr([]byte("replacement-certificate"))
		published.Status.IssuedAt = lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		updated, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, published)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(*updated.Status.Certificate)).To(Equal("historical-certificate"))
		Expect(updated.Status.IssuedAt).To(BeNil())
		current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(*current.Status.Certificate)).To(Equal("historical-certificate"))
		Expect(current.Status.IssuedAt).To(BeNil())
	})

	It("When an unissued CSR is recreated it should reject publication from its previous incarnation", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		request := CreateTestCSR()
		stale, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		deleted, err := provider.Delete(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		_, err = provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		stale.Status.Certificate = lo.ToPtr([]byte("previous-incarnation-certificate"))
		stale.Status.IssuedAt = lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		_, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, stale)
		Expect(err).To(MatchError(flterrors.ErrResourceVersionConflict))
		current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.Certificate).To(BeNil())
		Expect(current.Status.IssuedAt).To(BeNil())
	})

	DescribeTable("When an unissued CSR spec changes it should reject publication from the superseded spec", func(changeSigner bool) {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		request := CreateTestCSR()
		stale, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		replacement := CreateTestCSR()
		replacement.Metadata.Name = request.Metadata.Name
		if changeSigner {
			replacement.Spec.Request = request.Spec.Request
			replacement.Spec.SignerName = "flightctl.io/device-enrollment"
		}
		_, _, err = provider.Update(suite.Ctx, suite.OrgID, &replacement)
		Expect(err).NotTo(HaveOccurred())
		stale.Status.Certificate = lo.ToPtr([]byte("superseded-spec-certificate"))
		stale.Status.IssuedAt = lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		_, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, stale)
		Expect(err).To(MatchError(flterrors.ErrResourceVersionConflict))
		current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Spec.Request).To(Equal(replacement.Spec.Request))
		Expect(current.Spec.SignerName).To(Equal(replacement.Spec.SignerName))
		Expect(current.Status.Certificate).To(BeNil())
		Expect(current.Status.IssuedAt).To(BeNil())
	},
		Entry("replacement certificate request", false),
		Entry("replacement non-enrollment signer", true),
	)

	It("When concurrent publications compete it should return the same retained certificate and issuance time to both callers", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		request := CreateTestCSR()
		first, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		second, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		first.Status.Certificate = lo.ToPtr([]byte("first-candidate"))
		first.Status.IssuedAt = lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		second.Status.Certificate = lo.ToPtr([]byte("second-candidate"))
		second.Status.IssuedAt = lo.ToPtr(time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC))
		type publication struct {
			resource *domain.CertificateSigningRequest
			err      error
		}
		start := make(chan struct{})
		results := make(chan publication, 2)
		for _, candidate := range []*domain.CertificateSigningRequest{first, second} {
			go func() {
				<-start
				published, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, candidate)
				results <- publication{resource: published, err: err}
			}()
		}
		close(start)
		outcomes := []publication{<-results, <-results}
		stored, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		switch string(*stored.Status.Certificate) {
		case "first-candidate":
			Expect(*stored.Status.IssuedAt).To(Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)))
		case "second-candidate":
			Expect(*stored.Status.IssuedAt).To(Equal(time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)))
		default:
			Fail("publication did not retain either candidate")
		}
		for _, outcome := range outcomes {
			Expect(outcome.err).NotTo(HaveOccurred())
			Expect(outcome.resource.Status.Certificate).To(Equal(stored.Status.Certificate))
			Expect(outcome.resource.Status.IssuedAt).To(Equal(stored.Status.IssuedAt))
		}
		retried, err := provider.UpdateStatus(suite.Ctx, suite.OrgID, second)
		Expect(err).NotTo(HaveOccurred())
		Expect(retried.Status.Certificate).To(Equal(stored.Status.Certificate))
		Expect(retried.Status.IssuedAt).To(Equal(stored.Status.IssuedAt))
	})

	It("When deleting an issued credential it should reserve its name without exposing the deleted resource", func() {
		request := CreateTestCSR()
		_, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		deleted, err := provider.Delete(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		_, status = suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusNotFound))
		_, status = suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
	})

	It("When a standalone credential has no issuing CSR it should create an unbound enrollment request", func() {
		credential, err := suite.caClient.MakeClientCertificate(suite.Ctx, "", "", "standalone-credential", 1)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, credential.Certs[0])
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(BeNil())
	})

	It("When a standalone credential shares a retained CSR name it should not claim that CSR identity", func() {
		request := CreateTestCSR()
		retained, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		issued, err := fcrypto.ParseCertificatePEM(*retained.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		credential, err := suite.caClient.MakeClientCertificate(suite.Ctx, "", "", *request.Metadata.Name, 1)
		Expect(err).NotTo(HaveOccurred())
		leaf := credential.Certs[0]
		Expect(leaf.Raw).NotTo(Equal(issued.Raw))
		for _, extension := range leaf.Extensions {
			Expect(extension.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 4})).To(BeFalse())
		}
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(BeNil())
	})

	It("When an authenticated credential attests its CSR it should retain that reference even after CSR deletion", func() {
		request := CreateTestCSR()
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*created.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		_, err = provider.Delete(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		er, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		encoded, err := json.Marshal(er.Status)
		Expect(err).NotTo(HaveOccurred())
		var fields map[string]interface{}
		Expect(json.Unmarshal(encoded, &fields)).To(Succeed())
		Expect(fields["enrollmentCertificateName"]).To(Equal(*request.Metadata.Name))
	})

	It("When CSR database reads fail it should still bind signed identity but not invent a recovered identity", func() {
		request := CreateTestCSR()
		credential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		oldLeaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, "flightctl.io/enrollment"))
		Expect(err).NotTo(HaveOccurred())
		persistence := &unavailableCSRReads{
			Store: csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName),
		}
		service := suite.enrollmentServiceWithPersistence(persistence, erstore.NewEnrollmentRequestStore(suite.DB, suite.Log))
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		created, status := service.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(Equal(request.Metadata.Name))
		legacyCtx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, oldLeaf)
		unresolved, status := service.CreateEnrollmentRequest(legacyCtx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusInternalServerError))
		Expect(unresolved).To(BeNil())
	})

	It("When an old enrollment leaf uniquely matches retained issuance it should recover the CSR reference", func() {
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, "flightctl.io/enrollment"))
		Expect(err).NotTo(HaveOccurred())
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		stored.Status = &domain.CertificateSigningRequestStatus{Certificate: lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))}
		_, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, stored)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		er, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(er.Status.EnrollmentCertificateName).To(Equal(request.Metadata.Name))
	})

	DescribeTable("When recovering an old credential it should use only a unique exact issuing leaf in the authenticated tenant", func(localCopies int, foreignCopy bool, certificateKind string, expectedName *string) {
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, "flightctl.io/enrollment"))
		Expect(err).NotTo(HaveOccurred())
		retainedPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		switch certificateKind {
		case "same-cn-different-leaf":
			other, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
				signer.WithExtension(signer.OIDSignerName, "flightctl.io/enrollment"))
			Expect(err).NotTo(HaveOccurred())
			Expect(other.Subject.CommonName).To(Equal(leaf.Subject.CommonName))
			Expect(other.Raw).NotTo(Equal(leaf.Raw))
			retainedPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Raw})
		case "unreadable":
			retainedPEM = []byte("not-a-PEM-certificate")
		}
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		for index := 0; index < localCopies; index++ {
			retained := request
			retained.Metadata.Name = lo.ToPtr(fmt.Sprintf("retained-%d", index))
			if certificateKind == "different-signer" {
				retained.Spec.SignerName = "flightctl.io/device"
			}
			stored, err := provider.Create(suite.Ctx, suite.OrgID, &retained)
			Expect(err).NotTo(HaveOccurred())
			stored.Status.Certificate = &retainedPEM
			_, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, stored)
			Expect(err).NotTo(HaveOccurred())
		}
		if foreignCopy {
			foreignTenant := uuid.MustParse("33333333-3333-4333-8333-333333333333")
			_, err := suite.OrganizationStore.Create(suite.Ctx, &model.Organization{
				ID: foreignTenant, ExternalID: "other-credential-tenant", DisplayName: "Other credential tenant",
			})
			Expect(err).NotTo(HaveOccurred())
			stored, err := provider.Create(suite.Ctx, foreignTenant, &request)
			Expect(err).NotTo(HaveOccurred())
			stored.Status.Certificate = &retainedPEM
			_, err = provider.UpdateStatus(suite.Ctx, foreignTenant, stored)
			Expect(err).NotTo(HaveOccurred())
		}
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		er, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(er.Status.EnrollmentCertificateName).To(Equal(expectedName))
	},
		Entry("no retained match", 0, false, "exact", (*string)(nil)),
		Entry("ambiguous retained matches", 2, false, "exact", (*string)(nil)),
		Entry("only a foreign-tenant match", 0, true, "exact", (*string)(nil)),
		Entry("unique local match despite a foreign duplicate", 1, true, "exact", lo.ToPtr("retained-0")),
		Entry("matching common name with a different leaf", 1, false, "same-cn-different-leaf", (*string)(nil)),
		Entry("malformed retained certificate", 1, false, "unreadable", (*string)(nil)),
		Entry("a different CSR signer", 1, false, "different-signer", (*string)(nil)),
	)

	DescribeTable("When signed identity is malformed it should create an unbound request without legacy recovery", func(encoded []byte) {
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, "flightctl.io/enrollment"),
			func(certificate *x509.Certificate) error {
				certificate.ExtraExtensions = append(certificate.ExtraExtensions, pkix.Extension{
					Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 4}, Value: encoded,
				})
				return nil
			})
		Expect(err).NotTo(HaveOccurred())
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		stored.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
		_, err = provider.UpdateStatus(suite.Ctx, suite.OrgID, stored)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		er, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(er.Status.EnrollmentCertificateName).To(BeNil())
		read, status := suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *er.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(read.Status.EnrollmentCertificateName).To(BeNil())
	},
		Entry("invalid ASN.1", []byte{0xff}),
		Entry("trailing data", []byte{0x0c, 0x06, 'o', 'r', 'i', 'g', 'i', 'n', 0x00}),
		Entry("unsupported structured version", []byte{0x30, 0x0b, 0x02, 0x01, 0x02, 0x0c, 0x06, 'o', 'r', 'i', 'g', 'i', 'n'}),
		Entry("empty identity", []byte{0x0c, 0x00}),
	)

	DescribeTable("When a signed origin is presented it should respect signer and tenant boundaries", func(signerName, tenant string, expectedCode int, expectedName *string) {
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, signerName),
			signer.WithExtension(signer.OIDEnrollmentCSRName, "attested-origin"),
			func(certificate *x509.Certificate) error {
				if tenant == "" {
					return nil
				}
				return signer.WithExtension(signer.OIDOrgID, tenant)(certificate)
			})
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(expectedCode))
		if expectedCode != http.StatusCreated {
			Expect(created).To(BeNil())
			return
		}
		Expect(created.Status.EnrollmentCertificateName).To(Equal(expectedName))
		stored, status := suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(stored.Status.EnrollmentCertificateName).To(Equal(expectedName))
	},
		Entry("absent organization retains the default tenant", "flightctl.io/enrollment", "", http.StatusCreated, lo.ToPtr("attested-origin")),
		Entry("a foreign organization cannot bind", "flightctl.io/enrollment", "11111111-1111-4111-8111-111111111111", http.StatusCreated, (*string)(nil)),
		Entry("a malformed organization cannot bind", "flightctl.io/enrollment", "invalid-tenant", http.StatusCreated, (*string)(nil)),
		Entry("a management signer cannot authorize enrollment", "flightctl.io/device", "", http.StatusBadRequest, (*string)(nil)),
	)

	It("When replacing enrollment status it should preserve the server-owned origin reference", func() {
		request := CreateTestCSR()
		credential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		er, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		er.Status = &domain.EnrollmentRequestStatus{EnrollmentCertificateName: lo.ToPtr("forged"), Conditions: []domain.Condition{}}
		updated, status := suite.EnrollmentRequest.ReplaceEnrollmentRequestStatus(suite.Ctx, suite.OrgID, *er.Metadata.Name, *er)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(updated.Status.EnrollmentCertificateName).To(Equal(request.Metadata.Name))
		stored, status := suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *er.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(stored.Status.EnrollmentCertificateName).To(Equal(request.Metadata.Name))
	})

	It("When an old request has no origin it should ignore forged references and stay unbound through replacement and manual approval", func() {
		request := CreateTestER()
		request.Status = &domain.EnrollmentRequestStatus{EnrollmentCertificateName: lo.ToPtr("forged-origin")}
		created, status := enrollmentrequestservice.CreateEnrollmentRequestFromUntrusted(suite.Ctx, suite.EnrollmentRequest, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(BeNil())
		credential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, CreateTestCSR())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		replaced, status := enrollmentrequestservice.ReplaceEnrollmentRequestFromUntrusted(ctx, suite.EnrollmentRequest, suite.OrgID, *request.Metadata.Name, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(replaced.Status.EnrollmentCertificateName).To(BeNil())
		read, status := suite.EnrollmentRequest.GetEnrollmentRequest(ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(read.Status.EnrollmentCertificateName).To(BeNil())
		_, status = suite.EnrollmentRequest.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name,
			domain.EnrollmentRequestApproval{Approved: true, Labels: lo.ToPtr(map[string]string{})})
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		approved, status := suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(approved.Status.Certificate).NotTo(BeNil())
		Expect(approved.Status.EnrollmentCertificateName).To(BeNil())
	})

	It("When an enrollment request is recreated it should reject status from its previous incarnation", func() {
		request := CreateTestER()
		stale, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		status = suite.EnrollmentRequest.DeleteEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		replacement := CreateTestER()
		replacement.Metadata.Name = request.Metadata.Name
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, replacement)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.Certificate).To(BeNil())
		stale.Status.Certificate = lo.ToPtr("previous-incarnation-management-certificate")
		_, status = suite.EnrollmentRequest.ReplaceEnrollmentRequestStatus(suite.Ctx, suite.OrgID, *request.Metadata.Name, *stale)
		Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
		current, status := suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(current.Spec.Csr).To(Equal(replacement.Spec.Csr))
		Expect(current.Status.Certificate).To(BeNil())
		Expect(current.Status.EnrollmentCertificateName).To(BeNil())
	})

	It("When an ER is deleted during replacement it should not recreate a stale credential association", func() {
		credentialRequest := CreateTestCSR()
		credential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, credentialRequest)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		request := CreateTestER()
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(Equal(credentialRequest.Metadata.Name))
		persistence := &concurrentEnrollmentPersistence{
			Store: erstore.NewEnrollmentRequestStore(suite.DB, suite.Log),
			concurrentWrite: func() error {
				deleted := suite.EnrollmentRequest.DeleteEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
				if deleted.Code != http.StatusOK {
					return fmt.Errorf("concurrent deletion returned %d", deleted.Code)
				}
				return nil
			},
		}
		service := suite.enrollmentServiceWithPersistence(
			csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName), persistence)
		_, status = service.ReplaceEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, request)
		Expect(status.Code).To(Or(BeEquivalentTo(http.StatusNotFound), BeEquivalentTo(http.StatusConflict)))
		_, status = suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusNotFound))
	})

	It("When another credential creates an ER during replacement it should retain the actual creator origin", func() {
		firstRequest := CreateTestCSR()
		firstCredential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, firstRequest)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		firstLeaf, err := fcrypto.ParseCertificatePEM(*firstCredential.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		firstContext := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, firstLeaf)
		secondRequest := CreateTestCSR()
		secondCredential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, secondRequest)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		secondLeaf, err := fcrypto.ParseCertificatePEM(*secondCredential.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		secondContext := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, secondLeaf)
		request := CreateTestER()
		persistence := &concurrentEnrollmentPersistence{
			Store: erstore.NewEnrollmentRequestStore(suite.DB, suite.Log),
			concurrentWrite: func() error {
				_, created := suite.EnrollmentRequest.CreateEnrollmentRequest(secondContext, suite.OrgID, request)
				if created.Code != http.StatusCreated {
					return fmt.Errorf("concurrent creation returned %d", created.Code)
				}
				return nil
			},
		}
		service := suite.enrollmentServiceWithPersistence(
			csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName), persistence)
		replaced, status := service.ReplaceEnrollmentRequest(firstContext, suite.OrgID, *request.Metadata.Name, request)
		Expect(status.Code).To(Or(BeEquivalentTo(http.StatusOK), BeEquivalentTo(http.StatusConflict)))
		if status.Code == http.StatusOK {
			Expect(replaced.Status.EnrollmentCertificateName).To(Equal(secondRequest.Metadata.Name))
		}
		stored, status := suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(stored.Status.EnrollmentCertificateName).To(Equal(secondRequest.Metadata.Name))
	})

	It("When replacement creates a new enrollment request it should bind the authenticated issuing CSR", func() {
		request := CreateTestCSR()
		credential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		er := CreateTestER()
		created, status := suite.EnrollmentRequest.ReplaceEnrollmentRequest(ctx, suite.OrgID, *er.Metadata.Name, er)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(Equal(request.Metadata.Name))
	})

	It("When filtering enrollment requests by origin it should select references without asserting target availability", func() {
		request := CreateTestCSR()
		credential, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		leaf, err := fcrypto.ParseCertificatePEM(*credential.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		bound, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		_, status = suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		_, err = provider.Delete(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		requests, status := suite.EnrollmentRequest.ListEnrollmentRequests(suite.Ctx, suite.OrgID, domain.ListEnrollmentRequestsParams{
			FieldSelector: lo.ToPtr("status.enrollmentCertificateName"),
		})
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(requests.Items).To(HaveLen(1))
		Expect(requests.Items[0].Metadata.Name).To(Equal(bound.Metadata.Name))
	})
})
