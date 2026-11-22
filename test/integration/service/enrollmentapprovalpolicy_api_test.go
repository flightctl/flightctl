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
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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
	csrstore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	transport "github.com/flightctl/flightctl/internal/transport/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"gorm.io/gorm"
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

type blockedPolicyValidator struct {
	entered chan struct{}
	release <-chan struct{}
}

func (v blockedPolicyValidator) ValidatePolicySet(ctx context.Context, _ *domain.EnrollmentApprovalPolicy, _ []domain.EnrollmentApprovalPolicy) error {
	select {
	case v.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-v.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type policyReadRaceKey struct{}

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
	It("When credential revocation races service validation it should wait for the policy transaction", func() {
		ctx, cancel := context.WithTimeout(suite.Ctx, 10*time.Second)
		defer cancel()
		entered, release := make(chan struct{}, 1), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		service := policyservice.NewServiceHandler(policystore.NewStore(suite.DB, suite.Log), blockedPolicyValidator{entered, release}, suite.cfg.CA.DeviceEnrollmentSignerName, nil, suite.Log)
		var policy domain.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
		created, createDone := make(chan domain.Status, 1), make(chan struct{})
		go func() {
			defer close(createDone)
			_, status := service.CreateEnrollmentApprovalPolicy(ctx, suite.OrgID, policy)
			created <- status
		}()
		defer func() { cancel(); unblock(); Eventually(createDone, 5*time.Second).Should(BeClosed()) }()
		Eventually(entered, 5*time.Second).Should(Receive())
		var locked model.CertificateSigningRequest
		lockErr := suite.DB.WithContext(ctx).Raw("SELECT * FROM certificate_signing_requests WHERE org_id = ? AND name = ? FOR UPDATE NOWAIT", suite.OrgID, "commissioning").Scan(&locked).Error
		var pgErr *pgconn.PgError
		Expect(errors.As(lockErr, &pgErr)).To(BeTrue())
		Expect(pgErr.Code).To(Equal("55P03"))
		revoked, revokeDone := make(chan error, 1), make(chan struct{})
		credentials := csrstore.NewCertificateSigningRequestStore(suite.DB, suite.Log, suite.cfg.CA.DeviceEnrollmentSignerName)
		go func() {
			defer close(revokeDone)
			_, _, _, err := credentials.Mutate(ctx, suite.OrgID, "commissioning", nil, func(m *csrstore.CertificateSigningRequestMutation) error {
				return m.ApplyRevocation(domain.EnrollmentCertificateRevocation{Reason: "retired", RevokedAt: time.Now(), RevokedBy: "test"})
			})
			revoked <- err
		}()
		defer func() { cancel(); unblock(); Eventually(revokeDone, 5*time.Second).Should(BeClosed()) }()
		Consistently(revokeDone, 100*time.Millisecond).ShouldNot(BeClosed())
		unblock()
		var status domain.Status
		Eventually(created, 5*time.Second).Should(Receive(&status))
		Expect(status.Code).To(Equal(int32(201)), status.Message)
		var err error
		Eventually(revoked, 5*time.Second).Should(Receive(&err))
		Expect(err).NotTo(HaveOccurred())
		credential, err := credentials.Get(ctx, suite.OrgID, "commissioning")
		Expect(err).NotTo(HaveOccurred())
		Expect(credential.Status.HasRevocation()).To(BeTrue())
	})
	It("When accounting changes after policy load it should preserve the count and reject a stale write", func() {
		ctx, cancel := context.WithTimeout(suite.Ctx, 10*time.Second)
		defer cancel()
		policies := policystore.NewStore(suite.DB, suite.Log)
		service := policyservice.NewServiceHandler(policies, policyAPIValidator{}, suite.cfg.CA.DeviceEnrollmentSignerName, nil, suite.Log)
		var policy domain.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
		current, status := service.CreateEnrollmentApprovalPolicy(ctx, suite.OrgID, policy)
		Expect(status.Code).To(Equal(int32(201)))
		entered, release := make(chan struct{}, 1), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		service = policyservice.NewServiceHandler(policies, blockedPolicyValidator{entered, release}, suite.cfg.CA.DeviceEnrollmentSignerName, nil, suite.Log)
		current.Status = nil
		current.Spec.Enabled = true
		results, done := make(chan domain.Status, 1), make(chan struct{})
		go func() {
			defer close(done)
			_, status := service.ReplaceEnrollmentApprovalPolicy(ctx, suite.OrgID, "west", *current)
			results <- status
		}()
		defer func() { cancel(); unblock(); Eventually(done, 5*time.Second).Should(BeClosed()) }()
		Eventually(entered, 5*time.Second).Should(Receive())
		Expect(suite.DB.WithContext(ctx).Model(&model.EnrollmentApprovalPolicy{}).Where("org_id = ? AND name = ?", suite.OrgID, "west").Updates(map[string]interface{}{"status": model.MakeJSONField(domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: math.MaxInt64}), "resource_version": 2}).Error).To(Succeed())
		unblock()
		Eventually(results, 5*time.Second).Should(Receive(&status))
		Expect(status.Code).To(Equal(int32(409)), status.Message)
		preserved, err := policies.Get(ctx, suite.OrgID, "west")
		Expect(err).NotTo(HaveOccurred())
		Expect(preserved.Status.EnrollmentCount).To(Equal(int64(math.MaxInt64)))
		Expect(preserved.Metadata.ResourceVersion).To(Equal(lo.ToPtr("2")))
		Expect(preserved.Spec.Enabled).To(BeFalse())
	})
	for _, method := range []string{"PUT", "PATCH"} {
		It("When deletion races "+method+" after its initial read it should report the policy missing", func() {
			ctx, cancel := context.WithTimeout(suite.Ctx, 10*time.Second)
			defer cancel()
			policies := policystore.NewStore(suite.DB, suite.Log)
			service := policyservice.NewServiceHandler(policies, policyAPIValidator{}, suite.cfg.CA.DeviceEnrollmentSignerName, nil, suite.Log)
			var policy domain.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
			current, status := service.CreateEnrollmentApprovalPolicy(ctx, suite.OrgID, policy)
			Expect(status.Code).To(Equal(int32(201)))
			var fired atomic.Bool
			callbackName := "test:policy_delete_between_read_and_lock"
			Expect(suite.DB.Callback().Query().After("gorm:after_query").Register(callbackName, func(query *gorm.DB) {
				if query.Statement.Context.Value(policyReadRaceKey{}) == nil || query.Statement.Table != "enrollment_approval_policies" || query.Error != nil || !fired.CompareAndSwap(false, true) {
					return
				}
				Expect(policies.WithPolicySetTransaction(ctx, suite.OrgID, "commissioning", "west", func(txCtx context.Context, _ *domain.CertificateSigningRequest, _ []domain.EnrollmentApprovalPolicy) error {
					return policies.Delete(txCtx, suite.OrgID, "west")
				})).To(Succeed())
			})).To(Succeed())
			defer func() { Expect(suite.DB.Callback().Query().Remove(callbackName)).To(Succeed()) }()
			marked := context.WithValue(ctx, policyReadRaceKey{}, true)
			if method == "PUT" {
				current.Status = nil
				_, status = service.ReplaceEnrollmentApprovalPolicy(marked, suite.OrgID, "west", *current)
			} else {
				_, status = service.PatchEnrollmentApprovalPolicy(marked, suite.OrgID, "west", domain.PatchRequest{{Op: "test", Path: "/metadata/resourceVersion", Value: *current.Metadata.ResourceVersion}, {Op: "replace", Path: "/spec/enabled", Value: true}})
			}
			Expect(fired.Load()).To(BeTrue())
			Expect(status.Code).To(Equal(int32(404)), status.Message)
		})
	}

	for _, method := range []string{"PUT", "PATCH", "DELETE"} {
		It("When an existing policy loses its credential it should reject "+method+" as an invalid binding", func() {
			ctx, cancel := context.WithTimeout(suite.Ctx, 10*time.Second)
			defer cancel()
			policies := policystore.NewStore(suite.DB, suite.Log)
			service := policyservice.NewServiceHandler(policies, policyAPIValidator{}, suite.cfg.CA.DeviceEnrollmentSignerName, nil, suite.Log)
			var policy domain.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
			current, status := service.CreateEnrollmentApprovalPolicy(ctx, suite.OrgID, policy)
			Expect(status.Code).To(Equal(int32(201)))
			// Simulate missing credential data without altering the guarded CSR API.
			Expect(suite.DB.WithContext(ctx).Model(&model.CertificateSigningRequest{}).Where("org_id = ? AND name = ?", suite.OrgID, "commissioning").UpdateColumn("spec", nil).Error).To(Succeed())
			switch method {
			case "PUT":
				current.Status = nil
				_, status = service.ReplaceEnrollmentApprovalPolicy(ctx, suite.OrgID, "west", *current)
			case "PATCH":
				_, status = service.PatchEnrollmentApprovalPolicy(ctx, suite.OrgID, "west", domain.PatchRequest{{Op: "test", Path: "/metadata/resourceVersion", Value: *current.Metadata.ResourceVersion}, {Op: "replace", Path: "/spec/enabled", Value: true}})
			case "DELETE":
				status = service.DeleteEnrollmentApprovalPolicy(ctx, suite.OrgID, "west")
			}
			Expect(status.Code).To(Equal(int32(400)), status.Message)
			Expect(status.Reason).To(Equal("InvalidPolicy"))
			Expect(status.Message).To(ContainSubstring("spec.certificateName"))
			Expect(status.Message).To(ContainSubstring("commissioning"))
			retained, err := policies.Get(ctx, suite.OrgID, "west")
			Expect(err).NotTo(HaveOccurred())
			Expect(retained.Metadata.ResourceVersion).To(Equal(lo.ToPtr("1")))
			Expect(retained.Spec.Enabled).To(BeFalse())
		})
	}

	for _, field := range []string{"enabled", "requireTPMVerification"} {
		It("When a patch sets "+field+" to null it should reject without changing the policy", func() {
			handler := makeHandler(policyAPIValidator{})
			var policy api.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(policyJSON, &policy)).To(Succeed())
			policy.Spec.Enabled, policy.Spec.RequireTPMVerification = true, true
			body, err := json.Marshal(policy)
			Expect(err).NotTo(HaveOccurred())
			created := request(handler, http.MethodPost, "", body, suite.Ctx)
			Expect(created.Code).To(Equal(201), created.Body.String())
			Expect(json.Unmarshal(created.Body.Bytes(), &policy)).To(Succeed())
			body = []byte(fmt.Sprintf(`[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"replace","path":"/spec/%s","value":null}]`, *policy.Metadata.ResourceVersion, field))
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(body)).WithContext(suite.Ctx)
			req.Header.Set("Content-Type", "application/json-patch+json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			Expect(response.Code).To(Equal(400), response.Body.String())
			retained := request(handler, http.MethodGet, "/west", nil, suite.Ctx)
			Expect(retained.Code).To(Equal(200))
			Expect(retained.Body.String()).To(MatchJSON(created.Body.String()))
		})
	}

	DescribeTable("When policy numeric inputs cross the API it should enforce nullable int32 limits", func(value string, valid bool) {
		handler := makeHandler(policyAPIValidator{})
		var document map[string]interface{}
		Expect(json.Unmarshal(policyJSON, &document)).To(Succeed())
		spec := document["spec"].(map[string]interface{})
		if value != "omitted" {
			var limit interface{}
			Expect(json.Unmarshal([]byte(value), &limit)).To(Succeed())
			spec["approvalLimit"] = limit
		}
		body, err := json.Marshal(document)
		Expect(err).NotTo(HaveOccurred())
		response := request(handler, http.MethodPost, "", body, suite.Ctx)
		if !valid {
			Expect(response.Code).To(Equal(400), response.Body.String())
			response = request(handler, http.MethodPost, "", policyJSON, suite.Ctx)
		}
		Expect(response.Code).To(Equal(201), response.Body.String())
		var current api.EnrollmentApprovalPolicy
		Expect(json.Unmarshal(response.Body.Bytes(), &current)).To(Succeed())
		document["metadata"].(map[string]interface{})["resourceVersion"] = *current.Metadata.ResourceVersion
		body, err = json.Marshal(document)
		Expect(err).NotTo(HaveOccurred())
		response = request(handler, http.MethodPut, "/west", body, suite.Ctx)
		if valid {
			Expect(response.Code).To(Equal(200), response.Body.String())
		} else {
			Expect(response.Code).To(Equal(400), response.Body.String())
		}
		response = request(handler, http.MethodGet, "/west", nil, suite.Ctx)
		Expect(response.Code).To(Equal(200))
		Expect(json.Unmarshal(response.Body.Bytes(), &current)).To(Succeed())
		if value == "null" || value == "omitted" {
			current.Status = nil
			current.Spec.ApprovalLimit = lo.ToPtr(int32(1))
			body, err = json.Marshal(current)
			Expect(err).NotTo(HaveOccurred())
			response = request(handler, http.MethodPut, "/west", body, suite.Ctx)
			Expect(response.Code).To(Equal(200), response.Body.String())
			Expect(json.Unmarshal(response.Body.Bytes(), &current)).To(Succeed())
		}
		patchValue := value
		if value == "omitted" {
			patchValue = "null"
		}
		body = []byte(fmt.Sprintf(`[{"op":"test","path":"/metadata/resourceVersion","value":%q},{"op":"add","path":"/spec/approvalLimit","value":%s}]`, *current.Metadata.ResourceVersion, patchValue))
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", bytes.NewReader(body)).WithContext(suite.Ctx)
		req.Header.Set("Content-Type", "application/json-patch+json")
		patched := httptest.NewRecorder()
		handler.ServeHTTP(patched, req)
		if valid {
			Expect(patched.Code).To(Equal(200), patched.Body.String())
			var patchedPolicy api.EnrollmentApprovalPolicy
			Expect(json.Unmarshal(patched.Body.Bytes(), &patchedPolicy)).To(Succeed())
			if value == "null" || value == "omitted" {
				Expect(patchedPolicy.Spec.ApprovalLimit).To(BeNil())
			}
		} else {
			Expect(patched.Code).To(Equal(400), patched.Body.String())
		}
	},
		Entry("When omitted it should remain unlimited", "omitted", true),
		Entry("When null it should remain unlimited", "null", true),
		Entry("When zero it should remain explicit", "0", true),
		Entry("When maximal it should remain exact", "2147483647", true),
		Entry("When negative it should reject", "-1", false),
		Entry("When overflowing it should reject", "2147483648", false),
		Entry("When fractional it should reject", "0.5", false),
		Entry("When wrong type it should reject", `"bad"`, false),
	)
	It("When the issuing credential is missing it should identify the spec field", func() {
		handler := makeHandler(policyAPIValidator{})
		body := bytes.ReplaceAll(policyJSON, []byte("commissioning"), []byte("unknown-credential"))
		response := request(handler, http.MethodPost, "", body, suite.Ctx)
		Expect(response.Code).To(Equal(400), response.Body.String())
		var status api.Status
		Expect(json.Unmarshal(response.Body.Bytes(), &status)).To(Succeed())
		Expect(status.Reason).To(Equal("InvalidPolicy"))
		Expect(status.Message).To(ContainSubstring("spec.certificateName"))
		Expect(status.Message).To(ContainSubstring("unknown-credential"))
	})

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
		Expect(*created.Spec.ApprovalLimit).To(Equal(int32(10)))
		response = patch(privileged, `{"op":"replace","path":"/spec/approvalLimit","value":2147483647}`)
		Expect(response.Code).To(Equal(200), response.Body.String())
		Expect(json.Unmarshal(response.Body.Bytes(), &created)).To(Succeed())
		Expect(*created.Spec.ApprovalLimit).To(Equal(int32(2147483647)))
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
