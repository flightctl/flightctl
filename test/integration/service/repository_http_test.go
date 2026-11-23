package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store/model"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("Repository device delivery HTTP permissions", func() {
	It("When authenticated callers read or patch delivery it should enforce roles and mask secrets", func() {
		suite := NewServiceTestSuite()
		suite.Setup()
		DeferCleanup(suite.Teardown)

		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var role string
			switch r.Header.Get("Authorization") {
			case "Bearer repository-viewer":
				role = api.ExternalRoleViewer
			case "Bearer repository-operator":
				role = api.ExternalRoleOperator
			default:
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sub": role, "preferred_username": role, "roles": []string{role},
			})
		}))
		DeferCleanup(provider.Close)

		var roles api.AuthRoleAssignment
		Expect(roles.FromAuthDynamicRoleAssignment(api.AuthDynamicRoleAssignment{
			Type: api.AuthDynamicRoleAssignmentTypeDynamic, ClaimPath: []string{"roles"},
		})).To(Succeed())
		var organizations api.AuthOrganizationAssignment
		Expect(organizations.FromAuthStaticOrganizationAssignment(api.AuthStaticOrganizationAssignment{
			Type: api.AuthStaticOrganizationAssignmentTypeStatic, OrganizationName: "repository-http",
		})).To(Succeed())
		suite.cfg.Auth.OAuth2 = &api.OAuth2ProviderSpec{
			ProviderType: api.Oauth2, Enabled: lo.ToPtr(true),
			AuthorizationUrl: provider.URL + "/authorize", TokenUrl: provider.URL + "/token",
			UserinfoUrl: provider.URL + "/userinfo", Issuer: lo.ToPtr(provider.URL),
			ClientId: "repository-http", ClientSecret: "unused-by-userinfo-validation",
			UsernameClaim:  lo.ToPtr([]string{"preferred_username"}),
			RoleAssignment: roles, OrganizationAssignment: organizations,
		}
		suite.cfg.KV.Hostname = redisHost
		suite.cfg.KV.Port = redisPort
		suite.cfg.KV.Password = redisPassword
		certStore := filepath.Join(GinkgoT().TempDir(), "certs")
		Expect(os.MkdirAll(certStore, 0o700)).To(Succeed())
		suite.cfg.Service.CertStore = certStore
		suite.cfg.CA.InternalConfig.CertStore = certStore
		ca, serverCerts, _, err := testutil.NewTestCerts(suite.cfg)
		Expect(err).ToNot(HaveOccurred())
		queues := testutil.NewTestProvider(suite.Log)
		server, listener, err := testutil.NewTestApiServer(suite.Log, suite.cfg, suite.DB, ca, serverCerts, queues)
		Expect(err).ToNot(HaveOccurred())
		suite.cfg.Service.Address = listener.Addr().String()
		ctx, cancel := context.WithCancel(suite.Ctx)
		done := make(chan error, 1)
		go func() { done <- server.Run(ctx) }()
		DeferCleanup(func() {
			cancel()
			var runErr error
			Eventually(done, 30*time.Second).Should(Receive(&runErr))
			Expect(runErr).ToNot(HaveOccurred())
			queues.Stop()
			queues.Wait()
		})
		client, err := testutil.NewBareHTTPsClient(ca.GetCABundleX509(), nil)
		Expect(err).ToNot(HaveOccurred())
		client.Timeout = 10 * time.Second
		DeferCleanup(client.CloseIdleConnections)
		baseURL := "https://" + listener.Addr().String() + "/api/v1/repositories"
		request := func(method, path, token string, body any) (int, []byte, error) {
			GinkgoHelper()
			var reader io.Reader
			if body != nil {
				data, err := json.Marshal(body)
				Expect(err).ToNot(HaveOccurred())
				reader = bytes.NewReader(data)
			}
			req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
			Expect(err).ToNot(HaveOccurred())
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			if method == http.MethodPatch {
				req.Header.Set("Content-Type", "application/json-patch+json")
			}
			resp, err := client.Do(req)
			if err != nil {
				return 0, nil, err
			}
			defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
			data, err := io.ReadAll(resp.Body)
			return resp.StatusCode, data, err
		}
		Eventually(func(g Gomega) {
			status, body, err := request(http.MethodGet, "", "repository-operator", nil)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(status).To(Equal(http.StatusOK), string(body))
		}, 60*time.Second, 100*time.Millisecond).Should(Succeed())

		checkResponse := func(body []byte, expected bool) {
			GinkgoHelper()
			var repo api.Repository
			Expect(json.Unmarshal(body, &repo)).To(Succeed())
			spec, err := repo.Spec.AsOciRepoSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(spec.DevicePullAccess).To(Equal(lo.ToPtr(expected)))
			auth, err := spec.OciAuth.AsDockerAuth()
			Expect(err).ToNot(HaveOccurred())
			Expect(auth.Password).To(Equal(api.MaskedValuePlaceholder))
		}
		for _, mode := range []api.OciRepoSpecAccessMode{api.Read, api.ReadWrite} {
			name := "http-delivery-" + strings.ToLower(string(mode))
			var spec api.RepositorySpec
			var auth api.OciAuth
			Expect(auth.FromDockerAuth(api.DockerAuth{Username: "user", Password: "http-delivery-secret"})).To(Succeed())
			Expect(spec.FromOciRepoSpec(api.OciRepoSpec{
				Type: api.OciRepoSpecTypeOci, Registry: "quay.io", AccessMode: &mode,
				DevicePullAccess: lo.ToPtr(true), OciAuth: &auth,
			})).To(Succeed())
			status, body, err := request(http.MethodPost, "", "repository-operator", api.Repository{
				ApiVersion: "v1beta1", Kind: api.RepositoryKind, Metadata: api.ObjectMeta{Name: &name}, Spec: spec,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(status).To(Equal(http.StatusCreated), string(body))
			checkResponse(body, true)

			for _, enabled := range []bool{true, false} {
				By(fmt.Sprintf("checking %s viewer access with delivery=%t", mode, enabled))
				status, body, err = request(http.MethodGet, "/"+name, "repository-viewer", nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(status).To(Equal(http.StatusOK), string(body))
				checkResponse(body, enabled)

				var before model.Repository
				Expect(suite.DB.WithContext(suite.Ctx).Where("name = ?", name).First(&before).Error).ToNot(HaveOccurred())
				patch := []map[string]any{{"op": "replace", "path": "/spec/devicePullAccess", "value": !enabled}}
				status, body, err = request(http.MethodPatch, "/"+name, "repository-viewer", patch)
				Expect(err).ToNot(HaveOccurred())
				Expect(status).To(Equal(http.StatusForbidden), string(body))
				var after model.Repository
				Expect(suite.DB.WithContext(suite.Ctx).Where("name = ?", name).First(&after).Error).ToNot(HaveOccurred())
				Expect(after.Spec).To(Equal(before.Spec))
				Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))

				status, body, err = request(http.MethodPatch, "/"+name, "invalid-token", patch)
				Expect(err).ToNot(HaveOccurred())
				Expect(status).To(Equal(http.StatusUnauthorized), string(body))

				status, body, err = request(http.MethodPatch, "/"+name, "repository-operator", patch)
				Expect(err).ToNot(HaveOccurred())
				Expect(status).To(Equal(http.StatusOK), string(body))
				checkResponse(body, !enabled)
				Expect(suite.DB.WithContext(suite.Ctx).Where("name = ?", name).First(&after).Error).ToNot(HaveOccurred())
				storedSpec, err := after.Spec.Data.AsOciRepoSpec()
				Expect(err).ToNot(HaveOccurred())
				Expect(storedSpec.DevicePullAccess).To(Equal(lo.ToPtr(!enabled)))
				storedAuth, err := storedSpec.OciAuth.AsDockerAuth()
				Expect(err).ToNot(HaveOccurred())
				plaintext, encrypted, err := encryption.Decrypt(suite.Ctx, encryption.Ciphertext(storedAuth.Password))
				Expect(err).ToNot(HaveOccurred())
				Expect(encrypted).To(BeTrue())
				Expect(string(plaintext)).To(Equal("http-delivery-secret"))
			}
		}
	})
})
