package service_test

import (
	"bytes"
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
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/service/tpmcsr"
	"github.com/flightctl/flightctl/internal/store"
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	erstore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

func enrollmentIdentityFromCertificate(certificate *x509.Certificate) string {
	GinkgoHelper()
	var identity string
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 4}) {
			rest, err := asn1.Unmarshal(extension.Value, &identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(rest).To(BeEmpty())
		}
	}
	return identity
}

var _ = Describe("Enrollment credential identity", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		suite.Ctx = util.WithOrganizationID(suite.Ctx, suite.OrgID)
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
		request.Metadata.Name = lo.ToPtr("client-enrollment-edge-enrollment")
		request.Spec.Request = requestPEM
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.Certificate).NotTo(BeNil())
		stored, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, "client-enrollment-edge-enrollment")
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		leaf, err := fcrypto.ParseCertificatePEM(*stored.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		Expect(enrollmentIdentityFromCertificate(leaf)).To(Equal("client-enrollment-edge-enrollment"))
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
		Expect(enrollmentIdentityFromCertificate(issued)).To(Equal(*request.Metadata.Name))
	})

	It("When a trusted caller supplies its tenant context it should attest that enrollment CSR tenant", func() {
		tenant := uuid.MustParse("22222222-2222-4222-8222-222222222222")
		_, err := suite.OrganizationStore.Create(suite.Ctx, &model.Organization{
			ID: tenant, ExternalID: "enrollment-issuer-tenant", DisplayName: "Enrollment issuer tenant",
		})
		Expect(err).NotTo(HaveOccurred())
		ctx := util.WithOrganizationID(suite.Ctx, tenant)
		created, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(ctx, tenant, CreateTestCSR())
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

	It("When credential publication fails it should return the persistence error without a new signing failure", func() {
		persistence := &unavailableIssuancePersistence{
			Store: csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log),
		}
		eventService := events.NewServiceHandler(suite.EventStore, suite.workerClient, suite.Log)
		service := csrservice.NewServiceHandler(persistence, tpmcsr.NewVerifier(suite.EnrollmentRequest),
			suite.caClient, eventService, suite.Log, "", "")
		request := CreateTestCSR()
		created, status := service.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusInternalServerError))
		Expect(created).To(BeNil())
		stored, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(stored.Status.Certificate).To(BeNil())
		Expect(stored.Status.IssuedAt).To(BeNil())
		Expect(domain.IsStatusConditionTrue(stored.Status.Conditions, domain.ConditionTypeCertificateSigningRequestFailed)).To(BeFalse())
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
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
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
		_, status = suite.CertificateSigningRequest.UpdateCertificateSigningRequestApproval(suite.Ctx, suite.OrgID, *created.Metadata.Name, *created)
		Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
		current, status := suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *created.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(*current.Status.Certificate).To(Equal(certificate))
		Expect(current.Status.IssuedAt.Equal(issuedAt)).To(BeTrue())
	})

	DescribeTable("When deleting issued CSR names it should allow name reuse for every signer",
		func(signerName string, changeSigner bool) {
			provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
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
			published, err := seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
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
			recreated, err := provider.Create(suite.Ctx, suite.OrgID, &request)
			Expect(err).NotTo(HaveOccurred())
			Expect(recreated.Status.Certificate).To(BeNil())
			Expect(recreated.Status.IssuedAt).To(BeNil())
		},
		Entry("configured enrollment signer, historical leaf without identity metadata", "example.org/bootstrap", false),
		Entry("issued enrollment credential after a signer-spec change", "example.org/bootstrap", true),
		Entry("default name when enrollment is configured differently", "flightctl.io/enrollment", false),
		Entry("management signer", "flightctl.io/device-enrollment", false),
	)

	DescribeTable("When approval supplies certificate replacements it should sanitize them and commit conditions for every signer",
		func(signerName, expectedCertificate string) {
			provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
			request := CreateTestCSR()
			request.Spec.SignerName = signerName
			stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
			Expect(err).NotTo(HaveOccurred())
			stored.Status.Certificate = lo.ToPtr([]byte("first-publication"))
			published, err := seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
			Expect(err).NotTo(HaveOccurred())
			published.Status.Certificate = lo.ToPtr([]byte("replacement-publication"))
			published.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}}
			updated, err := approveCSRConditions(suite.csrServiceWithSigner(provider, "flightctl.io/device-svc-client"), suite.Ctx, suite.OrgID, published)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(*updated.Status.Certificate)).To(Equal(expectedCertificate))
			current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(*current.Status.Certificate)).To(Equal(expectedCertificate))
			Expect(domain.IsStatusConditionTrue(current.Status.Conditions, domain.ConditionTypeCertificateSigningRequestDenied)).To(BeTrue())
		},
		Entry("configured enrollment signer", "flightctl.io/device-svc-client", "first-publication"),
		Entry("default name is not enrollment when configured differently", "flightctl.io/enrollment", "first-publication"),
		Entry("management signer", "flightctl.io/device-enrollment", "first-publication"),
	)

	It("When a historical credential has no issuance time it should keep that time unknown on later status writes", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		request := CreateTestCSR()
		stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		stored.Status.Certificate = lo.ToPtr([]byte("historical-certificate"))
		published, err := seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
		Expect(err).NotTo(HaveOccurred())
		Expect(published.Status.IssuedAt).To(BeNil())
		published.Status.Certificate = lo.ToPtr([]byte("replacement-certificate"))
		published.Status.IssuedAt = lo.ToPtr(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		published.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestDenied, Status: domain.ConditionStatusTrue, Reason: "Denied"}}
		updated, err := approveCSRConditions(suite.CertificateSigningRequest, suite.Ctx, suite.OrgID, published)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(*updated.Status.Certificate)).To(Equal("historical-certificate"))
		Expect(updated.Status.IssuedAt).To(BeNil())
		current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(*current.Status.Certificate)).To(Equal("historical-certificate"))
		Expect(current.Status.IssuedAt).To(BeNil())
	})

	DescribeTable("When publication races a changed resource it should reject stale signing work", func(recreate, issued, changeSigner bool) {
		creationTime := time.Date(2026, 10, 18, 12, 0, 0, 123456000, time.UTC)
		database := suite.DB.Session(&gorm.Session{NowFunc: func() time.Time { return creationTime }})
		provider := csrstore.NewCertificateSigningRequestStore(database, suite.Log)
		request := CreateTestCSR()
		var replacement *domain.CertificateSigningRequest
		persistence := &concurrentCSRPublication{Store: provider, concurrentWrite: func(candidate *domain.CertificateSigningRequest) error {
			if recreate {
				if issued {
					current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
					if err != nil {
						return err
					}
					current.Status.Certificate = candidate.Status.Certificate
					current.Status.IssuedAt = candidate.Status.IssuedAt
					if _, err := seedCSRStatus(provider, suite.Ctx, suite.OrgID, current); err != nil {
						return err
					}
				}
				if _, err := provider.Delete(suite.Ctx, suite.OrgID, *request.Metadata.Name); err != nil {
					return err
				}
				creationTime = creationTime.Add(time.Microsecond)
				var err error
				replacement, err = provider.Create(suite.Ctx, suite.OrgID, &request)
				if err != nil {
					return err
				}
				replacement, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, replacement)
				if err != nil {
					return err
				}
				Expect(replacement.Metadata.ResourceVersion).To(Equal(candidate.Metadata.ResourceVersion))
				Expect(replacement.Metadata.CreationTimestamp).NotTo(Equal(candidate.Metadata.CreationTimestamp))
				return nil
			}
			changed := CreateTestCSR()
			changed.Metadata.Name = request.Metadata.Name
			if changeSigner {
				changed.Spec.Request = request.Spec.Request
				changed.Spec.SignerName = suite.caClient.Cfg.DeviceManagementSignerName
			}
			var err error
			replacement, _, err = provider.Update(suite.Ctx, suite.OrgID, &changed)
			return err
		}}
		service := suite.csrServiceWithSigner(persistence, suite.caClient.Cfg.DeviceEnrollmentSignerName)
		_, status := service.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
		current, err := provider.Get(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Spec).To(Equal(replacement.Spec))
		Expect(current.Status.Certificate).To(BeNil())
		Expect(current.Status.IssuedAt).To(BeNil())
	}, Entry("unissued delete and recreate", true, false, false), Entry("issued delete and recreate", true, true, false), Entry("changed CSR request", false, false, false), Entry("changed signer", false, false, true))

	It("When concurrent approvals publish different certificates it should return the committed first pair to both callers", func() {
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		request := CreateTestCSR()
		first, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		first.Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue, Reason: "Approved"}}
		type publication struct {
			resource *domain.CertificateSigningRequest
			status   domain.Status
		}
		prepared := make(chan *domain.CertificateSigningRequest, 2)
		results := make(chan publication, 2)
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		ctx, cancel := context.WithTimeout(suite.Ctx, 30*time.Second)
		defer cancel()
		for range 2 {
			candidate, err := store.CloneJSON(first)
			Expect(err).NotTo(HaveOccurred())
			persistence := &concurrentCSRPublication{Store: provider, concurrentWrite: func(candidate *domain.CertificateSigningRequest) error {
				select {
				case prepared <- candidate:
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			service := suite.csrServiceWithSigner(persistence, suite.caClient.Cfg.DeviceEnrollmentSignerName)
			go func() {
				resource, status := service.UpdateCertificateSigningRequestApproval(ctx, suite.OrgID, *candidate.Metadata.Name, *candidate)
				results <- publication{resource, status}
			}()
		}
		candidates := make([]*domain.CertificateSigningRequest, 0, 2)
		for range 2 {
			select {
			case candidate := <-prepared:
				candidates = append(candidates, candidate)
			case <-ctx.Done():
				Fail("concurrent signing did not reach publication before its deadline")
			}
		}
		Expect(candidates[0].Status.Certificate).NotTo(Equal(candidates[1].Status.Certificate))
		close(release)
		released = true
		outcomes := make([]publication, 0, 2)
		for range 2 {
			select {
			case outcome := <-results:
				outcomes = append(outcomes, outcome)
			case <-ctx.Done():
				Fail("concurrent publication exceeded its deadline")
			}
		}
		current, err := provider.Get(suite.Ctx, suite.OrgID, *first.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		for _, outcome := range outcomes {
			Expect(outcome.status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(outcome.resource.Status.Certificate).To(Equal(current.Status.Certificate))
			Expect(outcome.resource.Status.IssuedAt).To(Equal(current.Status.IssuedAt))
		}
		winningPair := false
		for _, candidate := range candidates {
			retained := bytes.Equal(*candidate.Status.Certificate, *current.Status.Certificate)
			if retained {
				winningPair = true
				Expect(current.Status.IssuedAt).To(Equal(candidate.Status.IssuedAt))
			}
			leaf, err := fcrypto.ParseCertificatePEM(*candidate.Status.Certificate)
			Expect(err).NotTo(HaveOccurred())
			resolved, err := suite.CertificateSigningRequest.FindNameByCertificate(suite.Ctx, suite.OrgID, leaf.Raw)
			Expect(err).NotTo(HaveOccurred())
			if retained {
				Expect(resolved).To(Equal(current.Metadata.Name))
			} else {
				Expect(resolved).To(BeNil())
			}
		}
		Expect(winningPair).To(BeTrue())
		_, status := suite.CertificateSigningRequest.UpdateCertificateSigningRequestApproval(suite.Ctx, suite.OrgID, *first.Metadata.Name, *first)
		Expect(status.Code).To(BeEquivalentTo(http.StatusConflict))
	})

	It("When deleting an issued credential it should remove it and allow fresh issuance under the same name", func() {
		request := CreateTestCSR()
		_, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		deleted, err := provider.Delete(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())
		_, status = suite.CertificateSigningRequest.GetCertificateSigningRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusNotFound))
		recreated, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(recreated.Status.Certificate).NotTo(BeNil())
		Expect(recreated.Status.IssuedAt).NotTo(BeNil())
	})

	It("When issuing a management credential from an Enrollment Request it should omit enrollment origin metadata", func() {
		request, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		_, status = suite.EnrollmentRequest.ApproveEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name,
			domain.EnrollmentRequestApproval{Approved: true, Labels: lo.ToPtr(map[string]string{})})
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		approved, status := suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(approved.Status.Certificate).NotTo(BeNil())
		leaf, err := fcrypto.ParseCertificatePEM([]byte(*approved.Status.Certificate))
		Expect(err).NotTo(HaveOccurred())
		for _, extension := range leaf.Extensions {
			Expect(extension.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 4})).To(BeFalse())
		}
	})

	It("When the enrollment helper issues a credential it should attest its request name without a CSR lookup", func() {
		credential, err := suite.caClient.MakeClientCertificate(suite.Ctx, "", "", "standalone-credential", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(enrollmentIdentityFromCertificate(credential.Certs[0])).To(Equal("standalone-credential"))
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, credential.Certs[0])
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(Equal(lo.ToPtr("standalone-credential")))
	})

	It("When an old credential has no issuing CSR it should create an unbound enrollment request", func() {
		request := CreateTestCSR()
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, "flightctl.io/enrollment"))
		Expect(err).NotTo(HaveOccurred())
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Status.EnrollmentCertificateName).To(BeNil())
	})

	It("When an old credential shares a retained CSR name but not its leaf it should not claim that CSR identity", func() {
		request := CreateTestCSR()
		retained, status := suite.CertificateSigningRequest.CreateCertificateSigningRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		issued, err := fcrypto.ParseCertificatePEM(*retained.Status.Certificate)
		Expect(err).NotTo(HaveOccurred())
		parsed, err := fcrypto.ParseCSR(request.Spec.Request)
		Expect(err).NotTo(HaveOccurred())
		parsed.Subject.CommonName = issued.Subject.CommonName
		leaf, err := suite.caClient.IssueRequestedClientCertificate(suite.Ctx, parsed, 3600,
			signer.WithExtension(signer.OIDSignerName, "flightctl.io/enrollment"))
		Expect(err).NotTo(HaveOccurred())
		Expect(leaf.Subject.CommonName).To(Equal(issued.Subject.CommonName))
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
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
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
			Store: csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log),
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
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		stored.Status = &domain.CertificateSigningRequestStatus{Certificate: lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))}
		_, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
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
		case "alternate-pem":
			retainedPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Comment": "historical encoding"}, Bytes: leaf.Raw})
		}
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		for index := 0; index < localCopies; index++ {
			retained := request
			retained.Metadata.Name = lo.ToPtr(fmt.Sprintf("retained-%d", index))
			if certificateKind == "different-signer" {
				retained.Spec.SignerName = "flightctl.io/device"
			}
			stored, err := provider.Create(suite.Ctx, suite.OrgID, &retained)
			Expect(err).NotTo(HaveOccurred())
			stored.Status.Certificate = &retainedPEM
			_, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
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
			_, err = seedCSRStatus(provider, suite.Ctx, foreignTenant, stored)
			Expect(err).NotTo(HaveOccurred())
		}
		ctx := context.WithValue(suite.Ctx, consts.TLSPeerCertificateCtxKey, leaf)
		er, status := suite.EnrollmentRequest.CreateEnrollmentRequest(ctx, suite.OrgID, CreateTestER())
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(er.Status.EnrollmentCertificateName).To(Equal(expectedName))
	},
		Entry("no retained match", 0, false, "exact", (*string)(nil)),
		Entry("ambiguous retained matches", 2, false, "exact", (*string)(nil)),
		Entry("ambiguous retained leaves after signer-spec changes", 2, false, "different-signer", (*string)(nil)),
		Entry("only a foreign-tenant match", 0, true, "exact", (*string)(nil)),
		Entry("unique local match despite a foreign duplicate", 1, true, "exact", lo.ToPtr("retained-0")),
		Entry("matching common name with a different leaf", 1, false, "same-cn-different-leaf", (*string)(nil)),
		Entry("malformed retained certificate", 1, false, "unreadable", (*string)(nil)),
		Entry("an alternate valid PEM encoding", 1, false, "alternate-pem", lo.ToPtr("retained-0")),
		Entry("a retained enrollment leaf with a changed CSR signer", 1, false, "different-signer", lo.ToPtr("retained-0")),
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
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
		stored, err := provider.Create(suite.Ctx, suite.OrgID, &request)
		Expect(err).NotTo(HaveOccurred())
		stored.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
		_, err = seedCSRStatus(provider, suite.Ctx, suite.OrgID, stored)
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
			csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log), persistence)
		_, status = service.ReplaceEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, request)
		Expect(status.Code).To(Or(BeEquivalentTo(http.StatusNotFound), BeEquivalentTo(http.StatusConflict)))
		_, status = suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusNotFound))
	})

	DescribeTable("When another credential creates an ER during replacement it should retain the actual creator status", func(bound bool) {
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
		var creatorStatus *domain.EnrollmentRequestStatus
		persistence := &concurrentEnrollmentPersistence{
			Store: erstore.NewEnrollmentRequestStore(suite.DB, suite.Log),
			concurrentWrite: func() error {
				creatorContext := suite.Ctx
				if bound {
					creatorContext = secondContext
				}
				creator, created := suite.EnrollmentRequest.CreateEnrollmentRequest(creatorContext, suite.OrgID, request)
				if created.Code != http.StatusCreated {
					return fmt.Errorf("concurrent creation returned %d", created.Code)
				}
				domain.SetStatusCondition(&creator.Status.Conditions, domain.Condition{
					Type: domain.ConditionTypeEnrollmentRequestApproved, Status: domain.ConditionStatusFalse,
					Reason: "PendingApproval", Message: "awaiting approval",
				})
				stored, status := suite.EnrollmentRequest.ReplaceEnrollmentRequestStatus(suite.Ctx, suite.OrgID, *creator.Metadata.Name, *creator)
				if status.Code != http.StatusOK {
					return fmt.Errorf("concurrent status returned %d", status.Code)
				}
				creatorStatus = stored.Status
				return nil
			},
		}
		service := suite.enrollmentServiceWithPersistence(
			csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log), persistence)
		replaced, status := service.ReplaceEnrollmentRequest(firstContext, suite.OrgID, *request.Metadata.Name, request)
		Expect(status.Code).To(Or(BeEquivalentTo(http.StatusOK), BeEquivalentTo(http.StatusConflict)))
		if status.Code == http.StatusOK {
			Expect(replaced.Status).To(Equal(creatorStatus))
		}
		stored, status := suite.EnrollmentRequest.GetEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(stored.Status).To(Equal(creatorStatus))
		if bound {
			Expect(stored.Status.EnrollmentCertificateName).To(Equal(secondRequest.Metadata.Name))
		} else {
			Expect(stored.Status.EnrollmentCertificateName).To(BeNil())
		}
	}, Entry("known origin and pending condition", true), Entry("absent origin and pending condition", false))

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

	DescribeTable("When replacing an ER it should retain numeric resource-version validation", func(version string, expectedCode int) {
		request := CreateTestER()
		created, status := suite.EnrollmentRequest.CreateEnrollmentRequest(suite.Ctx, suite.OrgID, request)
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated))
		Expect(created.Metadata.ResourceVersion).To(Equal(lo.ToPtr("1")))
		request.Metadata.ResourceVersion = &version
		_, status = suite.EnrollmentRequest.ReplaceEnrollmentRequest(suite.Ctx, suite.OrgID, *request.Metadata.Name, request)
		Expect(status.Code).To(BeEquivalentTo(expectedCode))
	}, Entry("equivalent numeric value", "01", http.StatusOK),
		Entry("malformed version", "invalid", http.StatusBadRequest),
		Entry("stale version", "2", http.StatusConflict))

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
		provider := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log)
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
