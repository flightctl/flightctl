package agent

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	agentapi "github.com/flightctl/flightctl/api/agent/v1beta1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	icrypto "github.com/flightctl/flightctl/internal/crypto"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/test/harness"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Server-owned enrollment status over HTTP", func() {
	var fixture *harness.TestHarness
	var ctx context.Context
	var management *apiclient.Client
	var enrollment *http.Client
	var agentURL string

	assertCanonicalOwnership := func() {
		core, err := api.GetSpec()
		Expect(err).NotTo(HaveOccurred())
		Expect(core.Components.Schemas["EnrollmentRequestStatus"].Value.Properties["enrollmentCertificateName"].Value.ReadOnly).To(BeFalse())
		Expect(core.Components.Schemas["CertificateSigningRequestStatus"].Value.Properties["issuedAt"].Value.ReadOnly).To(BeFalse())
		agent, err := agentapi.GetSpec()
		Expect(err).NotTo(HaveOccurred())
		for path, property := range map[string]string{"/enrollmentrequests": "enrollmentCertificateName", "/certificatesigningrequests": "issuedAt"} {
			resource := agent.Paths.Find(path).Post.RequestBody.Value.Content["application/json"].Schema.Value
			Expect(resource.Properties["status"].Value.Properties[property].Value.ReadOnly).To(BeFalse())
		}
	}

	BeforeEach(func() {
		assertCanonicalOwnership()
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(testutil.StartSpecTracerForGinkgo(suiteCtx), time.Minute)
		DeferCleanup(cancel)
		oidcProvider, managementToken := newEnrollmentHTTPAuth()
		var err error
		fixture, err = harness.NewTestHarness(ctx, GinkgoT().TempDir(), func(err error) { GinkgoWriter.Printf("HTTP fixture: %v\n", err) },
			harness.WithRedis(redisHost, redisPort, redisPassword), harness.WithoutAutoStartAgent(), harness.WithOIDCProvider(oidcProvider))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(fixture.Cleanup)
		management = fixture.Client.ClientInterface.(*apiclient.Client)
		management.Client = enrollmentManagementHTTPClient{client: management.Client, token: managementToken}
		certDir := filepath.Join(fixture.TestDirPath, "etc", "flightctl", "certs")
		roots, err := icrypto.LoadCACertsFromFile(filepath.Join(certDir, "client-signer.crt"))
		Expect(err).NotTo(HaveOccurred())
		credential, err := icrypto.GetTLSCertificateConfig(filepath.Join(certDir, "client-enrollment.crt"), filepath.Join(certDir, "client-enrollment.key"))
		Expect(err).NotTo(HaveOccurred())
		enrollment, err = testutil.NewBareHTTPsClient(roots, credential)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(enrollment.CloseIdleConnections)
		agentURL = strings.TrimRight(fixture.AgentConfig().EnrollmentService.Service.Server, "/") + "/api/v1"
		assertCanonicalOwnership()
	})
	AfterEach(assertCanonicalOwnership)

	request := func(client apiclient.HttpRequestDoer, baseURL, method, path string, body any, expected int, target any) {
		encoded, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(encoded))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		if method == http.MethodPatch {
			req.Header.Set("Content-Type", "application/json-patch+json")
		}
		response, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(expected), "%s %s: %s", method, path, payload)
		if target != nil {
			Expect(json.Unmarshal(payload, target)).To(Succeed())
		}
	}

	DescribeTable("When roundtripping an ER it should preserve the authoritative origin rather than a submitted reference", func(fromAgent bool) {
		credential, _ := newEnrollmentHTTPResource()
		er := api.EnrollmentRequest{ApiVersion: "v1beta1", Kind: "EnrollmentRequest", Metadata: credential.Metadata,
			Spec:   api.EnrollmentRequestSpec{Csr: string(credential.Spec.Request)},
			Status: &api.EnrollmentRequestStatus{Conditions: []api.Condition{}, EnrollmentCertificateName: lo.ToPtr("forged-origin")}}
		var stored api.EnrollmentRequest
		var expected *string
		baseURL := strings.TrimRight(management.Server, "/") + "/api/v1"
		if fromAgent {
			request(enrollment, agentURL, http.MethodPost, "/enrollmentrequests", er, http.StatusCreated, &stored)
			expected = lo.ToPtr("client-enrollment")
		} else {
			request(management.Client, baseURL, http.MethodPost, "/enrollmentrequests", er, http.StatusCreated, &stored)
		}
		Expect(stored.Status.EnrollmentCertificateName).To(Equal(expected))
		path := "/enrollmentrequests/" + *stored.Metadata.Name
		replacement := stored
		replacement.Status.EnrollmentCertificateName = lo.ToPtr("replacement-forgery")
		stored = api.EnrollmentRequest{}
		request(management.Client, baseURL, http.MethodPut, path, replacement, http.StatusOK, &stored)
		Expect(stored.Status.EnrollmentCertificateName).To(Equal(expected))
		statusUpdate := stored
		statusUpdate.Status.EnrollmentCertificateName = lo.ToPtr("status-forgery")
		stored = api.EnrollmentRequest{}
		request(management.Client, baseURL, http.MethodPut, path+"/status", statusUpdate, http.StatusOK, &stored)
		Expect(stored.Status.EnrollmentCertificateName).To(Equal(expected))
		patch := []map[string]any{{"op": "add", "path": "/status/enrollmentCertificateName", "value": "patch-forgery"}}
		request(management.Client, baseURL, http.MethodPatch, path, patch, http.StatusOK, &stored)
		request(management.Client, baseURL, http.MethodGet, path, nil, http.StatusOK, &stored)
		Expect(stored.Status.EnrollmentCertificateName).To(Equal(expected))
	}, Entry("known origin from authenticated agent creation", true), Entry("historical absence from management creation", false))

	It("When roundtripping an issued CSR it should ignore forged time and retain the committed issuance pair", func() {
		csr, _ := newEnrollmentHTTPResource()
		forged := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
		csr.Status = &api.CertificateSigningRequestStatus{Conditions: []api.Condition{}, IssuedAt: &forged}
		baseURL := strings.TrimRight(management.Server, "/") + "/api/v1"
		var stored api.CertificateSigningRequest
		request(management.Client, baseURL, http.MethodPost, "/certificatesigningrequests", csr, http.StatusCreated, &stored)
		Expect(stored.Status.IssuedAt).NotTo(BeNil())
		Expect(stored.Status.IssuedAt).NotTo(Equal(&forged))
		firstTime, firstCertificate := stored.Status.IssuedAt, stored.Status.Certificate
		path := "/certificatesigningrequests/" + *stored.Metadata.Name
		stored.Status.IssuedAt = &forged
		request(management.Client, baseURL, http.MethodPut, path, stored, http.StatusOK, &stored)
		Expect(stored.Status.IssuedAt).To(Equal(firstTime))
		Expect(stored.Status.Certificate).To(Equal(firstCertificate))
		request(management.Client, baseURL, http.MethodPut, path+"/approval", stored, http.StatusConflict, nil)
		request(management.Client, baseURL, http.MethodGet, path, nil, http.StatusOK, &stored)
		Expect(stored.Status.IssuedAt).To(Equal(firstTime))
	})

	It("When an authenticated device submits CSR status it should ignore issuance time on creation and approval", func() {
		csr, privateKey := newEnrollmentHTTPResource()
		er := api.EnrollmentRequest{ApiVersion: "v1beta1", Kind: "EnrollmentRequest", Metadata: csr.Metadata,
			Spec: api.EnrollmentRequestSpec{Csr: string(csr.Spec.Request)}}
		var enrolled api.EnrollmentRequest
		request(enrollment, agentURL, http.MethodPost, "/enrollmentrequests", er, http.StatusCreated, &enrolled)
		baseURL := strings.TrimRight(management.Server, "/") + "/api/v1"
		request(management.Client, baseURL, http.MethodPut, "/enrollmentrequests/"+*enrolled.Metadata.Name+"/approval", testutil.TestEnrollmentApproval(), http.StatusOK, nil)
		request(management.Client, baseURL, http.MethodGet, "/enrollmentrequests/"+*enrolled.Metadata.Name, nil, http.StatusOK, &enrolled)
		leaf, err := fcrypto.ParseCertificatePEM([]byte(*enrolled.Status.Certificate))
		Expect(err).NotTo(HaveOccurred())
		certDir := filepath.Join(fixture.TestDirPath, "etc", "flightctl", "certs")
		roots, err := icrypto.LoadCACertsFromFile(filepath.Join(certDir, "client-signer.crt"))
		Expect(err).NotTo(HaveOccurred())
		deviceClient, err := testutil.NewBareHTTPsClient(roots, &icrypto.TLSCertificateConfig{Certs: []*x509.Certificate{leaf}, Key: privateKey})
		Expect(err).NotTo(HaveOccurred())
		defer deviceClient.CloseIdleConnections()
		renewal := csr
		renewal.Metadata.Name = lo.ToPtr(*csr.Metadata.Name + "-renewal")
		renewal.Spec.SignerName = "flightctl.io/device-management-renewal"
		renewal.Status = &api.CertificateSigningRequestStatus{Conditions: []api.Condition{}, IssuedAt: lo.ToPtr(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))}
		var stored api.CertificateSigningRequest
		request(deviceClient, agentURL, http.MethodPost, "/certificatesigningrequests", renewal, http.StatusCreated, &stored)
		Expect(stored.Status.IssuedAt).To(BeNil())
		approval := stored
		approval.Status.IssuedAt = renewal.Status.IssuedAt
		approval.Status.Conditions = []api.Condition{{Type: api.ConditionTypeCertificateSigningRequestApproved, Status: api.ConditionStatusTrue, Reason: "Approved"}}
		stored = api.CertificateSigningRequest{}
		request(management.Client, baseURL, http.MethodPut, "/certificatesigningrequests/"+*approval.Metadata.Name+"/approval", approval, http.StatusOK, &stored)
		Expect(stored.Status.IssuedAt).To(BeNil())
		Expect(stored.Status.Certificate).NotTo(BeNil())
	})

	It("When unrelated read-only or invalid writable data is submitted it should still reject it", func() {
		baseURL := strings.TrimRight(management.Server, "/") + "/api/v1"
		device := map[string]any{"apiVersion": "v1beta1", "kind": "Device", "metadata": map[string]any{"name": "readonly-check"},
			"spec": map[string]any{"os": map[string]any{"image": "quay.io/flightctl/test:latest"}, "applications": []any{map[string]any{"name": "app", "appType": "container", "image": "quay.io/flightctl/app:latest", "annotations": map[string]string{"forged": "value"}}}}}
		var failure api.Status
		request(management.Client, baseURL, http.MethodPost, "/devices", device, http.StatusBadRequest, &failure)
		Expect(failure.Message).To(ContainSubstring("readOnly"))
		invalid := map[string]any{"apiVersion": "v1beta1", "kind": "CertificateSigningRequest", "metadata": map[string]any{"name": "invalid-spec"}, "spec": map[string]any{"signerName": 42, "request": ""}}
		request(management.Client, baseURL, http.MethodPost, "/certificatesigningrequests", invalid, http.StatusBadRequest, nil)
		csr, _ := newEnrollmentHTTPResource()
		invalidTime := map[string]any{"apiVersion": csr.ApiVersion, "kind": csr.Kind, "metadata": csr.Metadata, "spec": csr.Spec,
			"status": map[string]any{"conditions": []any{}, "issuedAt": "not-a-timestamp"}}
		request(management.Client, baseURL, http.MethodPost, "/certificatesigningrequests", invalidTime, http.StatusBadRequest, nil)
		invalidReference := api.EnrollmentRequest{ApiVersion: "v1beta1", Kind: "EnrollmentRequest", Metadata: csr.Metadata,
			Spec:   api.EnrollmentRequestSpec{Csr: string(csr.Spec.Request)},
			Status: &api.EnrollmentRequestStatus{Conditions: []api.Condition{}, EnrollmentCertificateName: lo.ToPtr("INVALID/REFERENCE")}}
		request(management.Client, baseURL, http.MethodPost, "/enrollmentrequests", invalidReference, http.StatusBadRequest, nil)
		request(enrollment, agentURL, http.MethodPost, "/enrollmentrequests", invalidReference, http.StatusBadRequest, nil)
	})
})

func newEnrollmentHTTPResource() (api.CertificateSigningRequest, crypto.PrivateKey) {
	publicKey, privateKey, err := fcrypto.NewKeyPair()
	Expect(err).NotTo(HaveOccurred())
	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	Expect(err).NotTo(HaveOccurred())
	name := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash))
	encoded, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: name}}, privateKey)
	Expect(err).NotTo(HaveOccurred())
	return api.CertificateSigningRequest{ApiVersion: "v1beta1", Kind: "CertificateSigningRequest", Metadata: api.ObjectMeta{Name: &name},
		Spec: api.CertificateSigningRequestSpec{SignerName: "flightctl.io/enrollment", Request: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: encoded}), Usages: lo.ToPtr([]string{"clientAuth", "CA:false"})}}, privateKey
}
