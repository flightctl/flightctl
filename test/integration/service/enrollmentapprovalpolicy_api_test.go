package service_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	server "github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	transport "github.com/flightctl/flightctl/internal/transport/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

type policyAPIValidator struct{}

func (policyAPIValidator) ValidatePolicySet(context.Context, *domain.EnrollmentApprovalPolicy, []domain.EnrollmentApprovalPolicy) error {
	return nil
}

type policyAPIAuthorizer struct {
	auth.AuthZMiddleware
	static    *authz.StaticAuthZ
	denyQuota bool
}

func (authorizer policyAPIAuthorizer) CheckPermission(ctx context.Context, resource, operation string) (bool, error) {
	if authorizer.denyQuota && resource == "enrollmentapprovalpolicies/approvallimit" {
		return false, nil
	}
	return authorizer.static.CheckPermission(ctx, resource, operation)
}

var _ = Describe("EnrollmentApprovalPolicyAPI", func() {
	var suite *ServiceTestSuite
	BeforeEach(func() {
		suite = &ServiceTestSuite{}
		suite.Setup()
		suite.Ctx = util.WithOrganizationID(suite.Ctx, suite.OrgID)
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
		Expect(err).NotTo(HaveOccurred())
		issued := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		Expect(suite.DB.Create(&model.CertificateSigningRequest{Resource: model.Resource{OrgID: suite.OrgID, Name: "commissioning"}, Spec: model.MakeJSONField(domain.CertificateSigningRequestSpec{SignerName: suite.cfg.CA.DeviceEnrollmentSignerName}), Status: model.MakeJSONField(domain.CertificateSigningRequestStatus{Certificate: &issued, IssuedAt: lo.ToPtr(time.Now())})}).Error).To(Succeed())
	})
	AfterEach(func() { suite.Teardown() })
	makeHandler := func(validator policyservice.PolicySetValidator, denyQuota ...bool) http.Handler {
		policies := policyservice.NewServiceHandler(policystore.NewStore(suite.DB, suite.Log), validator, suite.cfg.CA.DeviceEnrollmentSignerName, nil, suite.Log)
		authorizer := policyAPIAuthorizer{static: authz.NewStaticAuthZ(suite.Log)}
		if len(denyQuota) != 0 {
			authorizer.denyQuota = denyQuota[0]
		}
		handler := transport.NewTransportHandler(nil, nil, nil, policies, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, convert.NewConverter(), nil, nil, nil, authorizer)
		router := chi.NewRouter()
		router.Use(auth.CreateAuthZMiddleware(authorizer, suite.Log))
		specification, err := api.GetSpec()
		Expect(err).NotTo(HaveOccurred())
		router.Use(oapimiddleware.OapiRequestValidatorWithOptions(specification, &oapimiddleware.Options{SilenceServersWarning: true}))
		return server.HandlerWithOptions(handler, server.ChiServerOptions{BaseURL: "/api/v1", BaseRouter: router})
	}
	request := func(handler http.Handler, method, path string, body []byte, ctx context.Context) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/enrollmentapprovalpolicies"+path, bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, req)
		return response
	}
	policyJSON := []byte(`{"apiVersion":"v1beta1","kind":"EnrollmentApprovalPolicy","metadata":{"name":"west"},"spec":{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{}}}`)
	DescribeTable("When a credential becomes invalid it should preserve cleanup boundaries", func(state, method string, unavailable bool) {
		handler := makeHandler(policyAPIValidator{})
		var candidate api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &candidate)).To(Succeed())
		candidate.Spec.Enabled = true
		body, err := json.Marshal(candidate)
		Expect(err).NotTo(HaveOccurred())
		response := request(handler, http.MethodPost, "", body, suite.Ctx)
		Expect(response.Code).To(Equal(201), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &candidate)).To(Succeed())
		Expect(suite.DB.Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", suite.OrgID, "west").UpdateColumn("status", model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: 9})).Error).To(Succeed())
		var credential model.CertificateSigningRequest
		Expect(suite.DB.Where("org_id = ? AND name = ?", suite.OrgID, "commissioning").First(&credential).Error).To(Succeed())
		credentialStatus := credential.Status.Data
		if state == "revoked" {
			credentialStatus.Revocation = &domain.EnrollmentCertificateRevocation{}
		} else {
			key, keyErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			Expect(keyErr).NotTo(HaveOccurred())
			certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(-time.Hour)}
			der, certErr := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
			Expect(certErr).NotTo(HaveOccurred())
			credentialStatus.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		}
		Expect(suite.DB.Model(&credential).UpdateColumn("status", model.MakeJSONField(credentialStatus)).Error).To(Succeed())
		candidate.Status = nil
		candidate.Spec.Enabled = false
		body, err = json.Marshal(candidate)
		Expect(err).NotTo(HaveOccurred())
		if method == http.MethodPatch {
			body = []byte(`[{"op":"test","path":"/metadata/resourceVersion","value":"` + *candidate.Metadata.ResourceVersion + `"},{"op":"replace","path":"/spec/enabled","value":false}]`)
		}
		if unavailable {
			handler = makeHandler(policyservice.UnavailablePolicySetValidator{})
		}
		send := func(ctx context.Context) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			if method == http.MethodPatch {
				req.Header.Set("Content-Type", "application/json-patch+json")
			}
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, req)
			return result
		}
		Expect(send(util.WithOrganizationID(suite.Ctx, uuid.New())).Code).To(Equal(404))
		organization := &model.Organization{ID: suite.OrgID}
		viewer := identity.NewMappedIdentity("viewer", "viewer", []*model.Organization{organization}, map[string][]string{suite.OrgID.String(): {api.RoleViewer}}, false, nil)
		Expect(send(context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, viewer)).Code).To(Equal(403))
		response = send(suite.Ctx)
		var retained model.EnrollmentApprovalPolicy
		Expect(suite.DB.Unscoped().Where("org_id = ? AND name = ?", suite.OrgID, "west").First(&retained).Error).To(Succeed())
		Expect(retained.Status.Data.EnrollmentCount).To(Equal(int64(9)))
		var refreshed model.CertificateSigningRequest
		Expect(suite.DB.Where("org_id = ? AND name = ?", suite.OrgID, "commissioning").First(&refreshed).Error).To(Succeed())
		if unavailable {
			Expect(response.Code).To(Equal(503), response.Body.String())
			Expect(response.Body.String()).To(ContainSubstring("PolicyValidationUnavailable"))
			Expect(retained.Spec.Data.Enabled).To(BeTrue())
			Expect(retained.DeletedAt.Valid).To(BeFalse())
			Expect(refreshed.PolicySetRevision).To(Equal(credential.PolicySetRevision))
		} else {
			Expect(response.Code).To(Equal(200), response.Body.String())
			Expect(refreshed.PolicySetRevision).To(Equal(credential.PolicySetRevision + 1))
			if method == http.MethodDelete {
				Expect(retained.DeletedAt.Valid).To(BeTrue())
				Expect(request(handler, http.MethodPost, "", policyJSON, suite.Ctx).Code).To(Equal(409))
			} else {
				Expect(retained.Spec.Data.Enabled).To(BeFalse())
				Expect(retained.DeletedAt.Valid).To(BeFalse())
			}
		}
	},
		Entry("revoked PUT", "revoked", http.MethodPut, false),
		Entry("revoked PATCH", "revoked", http.MethodPatch, false),
		Entry("revoked DELETE", "revoked", http.MethodDelete, false),
		Entry("expired PUT", "expired", http.MethodPut, false),
		Entry("expired PATCH", "expired", http.MethodPatch, false),
		Entry("expired DELETE", "expired", http.MethodDelete, false),
		Entry("unavailable revoked PUT", "revoked", http.MethodPut, true),
		Entry("unavailable revoked PATCH", "revoked", http.MethodPatch, true),
		Entry("unavailable revoked DELETE", "revoked", http.MethodDelete, true),
		Entry("unavailable expired PUT", "expired", http.MethodPut, true),
		Entry("unavailable expired PATCH", "expired", http.MethodPatch, true),
		Entry("unavailable expired DELETE", "expired", http.MethodDelete, true),
	)
	It("When quota permission is denied it should reject quota mutations and unsupported source operations without writes", func() {
		privileged := makeHandler(policyAPIValidator{})
		limited := makeHandler(policyAPIValidator{}, true)
		response := request(privileged, http.MethodPost, "", policyJSON, suite.Ctx)
		Expect(response.Code).To(Equal(201), response.Body.String())
		var created api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		patch := func(handler http.Handler, operation string) *httptest.ResponseRecorder {
			body := []byte(`[{"op":"test","path":"/metadata/resourceVersion","value":"` + *created.Metadata.ResourceVersion + `"},` + operation + `]`)
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(body)).WithContext(suite.Ctx)
			req.Header.Set("Content-Type", "application/json-patch+json")
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, req)
			return result
		}
		response = patch(privileged, `{"op":"add","path":"/spec/approvalLimit","value":10}`)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		before := request(limited, http.MethodGet, "/west", nil, suite.Ctx).Body.String()
		for _, operation := range []string{
			`{"op":"add","path":"/spec/approvalLimit","value":12}`,
			`{"op":"replace","path":"/spec/approvalLimit","value":12}`,
			`{"op":"remove","path":"/spec/approvalLimit"}`,
			`{"op":"replace","path":"/spec","value":{"certificateName":"commissioning","matchExpression":"true","deviceLabels":{}}}`,
		} {
			response = patch(limited, operation)
			Expect(response.Code).To(Equal(403), response.Body.String())
			Expect(request(limited, http.MethodGet, "/west", nil, suite.Ctx).Body.String()).To(Equal(before))
		}
		for _, operation := range []string{
			`{"op":"move","from":"/spec/approvalLimit","path":"/spec/autoApprovalWindowDays"}`,
			`{"op":"copy","from":"/spec/approvalLimit","path":"/spec/autoApprovalWindowDays"}`,
		} {
			response = patch(limited, operation)
			Expect(response.Code).To(Equal(400), response.Body.String())
			Expect(request(limited, http.MethodGet, "/west", nil, suite.Ctx).Body.String()).To(Equal(before))
		}
		response = patch(limited, `{"op":"add","path":"/spec/autoApprovalWindowDays","value":10}`)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		Expect(*created.Spec.ApprovalLimit).To(Equal(int64(10)))
		response = patch(privileged, `{"op":"replace","path":"/spec/approvalLimit","value":9223372036854775807}`)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		Expect(*created.Spec.ApprovalLimit).To(Equal(int64(9223372036854775807)))
		before = request(privileged, http.MethodGet, "/west", nil, suite.Ctx).Body.String()
		for _, operation := range []string{
			`{"op":"replace","path":"/spec/approvalLimit","value":9223372036854775808}`,
			`{"op":"add","path":"/spec/unknown","value":true}`,
		} {
			response = patch(privileged, operation)
			Expect(response.Code).To(Equal(400), response.Body.String())
			Expect(request(privileged, http.MethodGet, "/west", nil, suite.Ctx).Body.String()).To(Equal(before))
		}
	})
	It("When real validation is unavailable it should return 503 without storing even a disabled draft", func() {
		handler := makeHandler(policyservice.UnavailablePolicySetValidator{})
		response := request(handler, http.MethodPost, "", policyJSON, suite.Ctx)
		Expect(response.Code).To(Equal(503), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("PolicyValidationUnavailable"))
		Expect(request(handler, http.MethodGet, "/west", nil, suite.Ctx).Code).To(Equal(404))
	})
	It("When authorized lifecycle uses a test-only provider it should protect versions, bindings and status", func() {
		handler := makeHandler(policyAPIValidator{})
		response := request(handler, http.MethodPost, "", policyJSON, suite.Ctx)
		Expect(response.Code).To(Equal(201), response.Body.String())
		var created api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		Expect(created.Status).NotTo(BeNil())
		Expect(created.Status.EnrollmentCount).To(Equal(int64(0)))
		response = request(handler, http.MethodGet, "?limit=1&fieldSelector=metadata.name%3Dwest", nil, suite.Ctx)
		Expect(response.Code).To(Equal(200), response.Body.String())
		var listed api.EnrollmentApprovalPolicyList
		Expect(json.Unmarshal(response.Body.Bytes(), &listed)).To(Succeed())
		Expect(listed.Items).To(HaveLen(1))
		Expect(listed.Items[0].Metadata.Name).To(Equal(created.Metadata.Name))
		Expect(listed.Items[0].Status.EnrollmentCount).To(Equal(int64(0)))
		response = request(handler, http.MethodGet, "?fieldSelector=spec.unknown%3Dx", nil, suite.Ctx)
		Expect(response.Code).To(Equal(400), response.Body.String())
		response = request(handler, http.MethodGet, "/west/status", nil, suite.Ctx)
		Expect(response.Code).To(Equal(200))
		Expect(response.Body.String()).To(MatchJSON(`{"enrollmentCount":0}`))
		Expect(request(handler, http.MethodPut, "/west", policyJSON, suite.Ctx).Code).To(Equal(409))
		foreignContext := util.WithOrganizationID(suite.Ctx, uuid.New())
		Expect(request(handler, http.MethodGet, "/west", nil, foreignContext).Code).To(Equal(404))
		Expect(request(handler, http.MethodGet, "/west/status", nil, foreignContext).Code).To(Equal(404))
		response = request(handler, http.MethodGet, "", nil, foreignContext)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &listed)).To(Succeed())
		Expect(listed.Items).To(BeEmpty())
		created.Status = nil
		created.Spec.Enabled = true
		changed, err := json.Marshal(created)
		Expect(err).NotTo(HaveOccurred())
		response = request(handler, http.MethodPut, "/west", changed, suite.Ctx)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		patch := []byte(`[{"op":"test","path":"/metadata/resourceVersion","value":"` + *created.Metadata.ResourceVersion + `"},{"op":"replace","path":"/spec/enabled","value":false}]`)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(patch)).WithContext(suite.Ctx)
		req.Header.Set("Content-Type", "application/json-patch+json")
		patched := httptest.NewRecorder()
		handler.ServeHTTP(patched, req)
		Expect(patched.Code).To(Equal(200), patched.Body.String())
		Expect(json.Unmarshal(patched.Body.Bytes(), &created)).To(Succeed())
		created.Status = nil
		created.Spec.CertificateName = "other"
		changed, err = json.Marshal(created)
		Expect(err).NotTo(HaveOccurred())
		Expect(request(handler, http.MethodPut, "/west", changed, suite.Ctx).Code).To(Equal(409))
		organization := &model.Organization{ID: suite.OrgID}
		viewer := identity.NewMappedIdentity("viewer", "viewer", []*model.Organization{organization}, map[string][]string{suite.OrgID.String(): {api.RoleViewer}}, false, nil)
		viewerCtx := context.WithValue(suite.Ctx, consts.MappedIdentityCtxKey, viewer)
		Expect(request(handler, http.MethodPost, "", policyJSON, viewerCtx).Code).To(Equal(403))
		Expect(request(handler, http.MethodDelete, "/west", nil, suite.Ctx).Code).To(Equal(200))
		Expect(request(handler, http.MethodPost, "", policyJSON, suite.Ctx).Code).To(Equal(409))
	})
})
