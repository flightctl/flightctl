package service_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// registryHostFromURL extracts "host:port" from a test server URL.
func registryHostFromURL(serverURL string) string {
	u, _ := url.Parse(serverURL)
	return u.Host
}

// mockOCIRegistryHandler returns an http.Handler that implements the minimal OCI
// Distribution Spec needed by the service's CheckRepositoryOci* methods:
//
//   - GET /v2/              → 200  (registry ping / ORAS auth probe)
//   - GET /v2/.../tags/list → 200  {"tags":[]}
//   - HEAD|GET /v2/.../manifests/known-tag → 200 with a valid OCI manifest + digest
//   - HEAD|GET /v2/.../manifests/<other>   → 404 OCI error
func mockOCIRegistryHandler() http.Handler {
	// A minimal OCI image manifest (index).
	manifestBody := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestBody))

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		case path == "/v2/" || path == "/v2":
			// Registry ping.
			w.WriteHeader(http.StatusOK)

		case strings.HasSuffix(path, "/tags/list"):
			// Tag listing – empty list is a valid, accessible response.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "image", "tags": []string{}})

		case strings.HasSuffix(path, "/manifests/known-tag"):
			// Known tag – return a minimal manifest so ORAS can resolve it.
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(manifestBody)))
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(manifestBody)
			}

		default:
			// Unknown manifest / anything else → 404.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []map[string]interface{}{
					{"code": "MANIFEST_UNKNOWN", "message": "manifest unknown"},
				},
			})
		}
	})

	return mux
}

var _ = Describe("Repository OCI check endpoints", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	Describe("CheckRepositoryOciTag", func() {
		When("the repository does not exist", func() {
			It("should return 404", func() {
				_, status := suite.Repository.CheckRepositoryOciTag(suite.Ctx, suite.OrgID, "nonexistent", "quay.io/myorg/myimage", "latest")
				Expect(status.Code).To(Equal(int32(http.StatusNotFound)))
			})
		})

		When("the repository is not of OCI type", func() {
			It("should return 400", func() {
				spec := api.RepositorySpec{}
				err := spec.FromGitRepoSpec(api.GitRepoSpec{
					Url:  "https://github.com/flightctl/flightctl.git",
					Type: api.GitRepoSpecTypeGit,
				})
				Expect(err).ToNot(HaveOccurred())
				_, status := suite.Repository.CreateRepository(suite.Ctx, suite.OrgID, api.Repository{
					ApiVersion: "v1beta1",
					Kind:       "Repository",
					Metadata:   api.ObjectMeta{Name: lo.ToPtr("git-repo")},
					Spec:       spec,
				})
				Expect(status.Code).To(Equal(int32(http.StatusCreated)))

				_, status = suite.Repository.CheckRepositoryOciTag(suite.Ctx, suite.OrgID, "git-repo", "quay.io/myorg/myimage", "latest")
				Expect(status.Code).To(Equal(int32(http.StatusBadRequest)))
				Expect(status.Message).To(ContainSubstring("not OCI"))
			})
		})

		When("the repository is OCI type and the tag exists in the registry", func() {
			It("should return 200 with accessible=true", func() {
				server := httptest.NewServer(mockOCIRegistryHandler())
				DeferCleanup(server.Close)

				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					Registry: registryHostFromURL(server.URL),
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Http),
				})
				Expect(err).ToNot(HaveOccurred())
				_, status := suite.Repository.CreateRepository(suite.Ctx, suite.OrgID, api.Repository{
					ApiVersion: "v1beta1",
					Kind:       "Repository",
					Metadata:   api.ObjectMeta{Name: lo.ToPtr("oci-repo")},
					Spec:       spec,
				})
				Expect(status.Code).To(Equal(int32(http.StatusCreated)))

				result, status := suite.Repository.CheckRepositoryOciTag(suite.Ctx, suite.OrgID, "oci-repo", "myimage", "known-tag")
				Expect(status.Code).To(Equal(int32(http.StatusOK)))
				Expect(result).ToNot(BeNil())
				Expect(result.Accessible).To(BeTrue())
			})
		})

		When("the repository is OCI type but the tag does not exist", func() {
			// The OCI registry returns 404 for an unknown tag; the endpoint itself
			// always returns HTTP 200 – callers inspect accessible+errorCode instead.
			It("should return 200 with accessible=false and errorCode 404", func() {
				server := httptest.NewServer(mockOCIRegistryHandler())
				DeferCleanup(server.Close)

				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					Registry: registryHostFromURL(server.URL),
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Http),
				})
				Expect(err).ToNot(HaveOccurred())
				_, status := suite.Repository.CreateRepository(suite.Ctx, suite.OrgID, api.Repository{
					ApiVersion: "v1beta1",
					Kind:       "Repository",
					Metadata:   api.ObjectMeta{Name: lo.ToPtr("oci-repo")},
					Spec:       spec,
				})
				Expect(status.Code).To(Equal(int32(http.StatusCreated)))

				result, status := suite.Repository.CheckRepositoryOciTag(suite.Ctx, suite.OrgID, "oci-repo", "myimage", "unknown-tag")
				Expect(status.Code).To(Equal(int32(http.StatusOK)))
				Expect(result).ToNot(BeNil())
				Expect(result.Accessible).To(BeFalse())
				Expect(result.ErrorCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Describe("CheckRepositoryOciImage", func() {
		When("the repository does not exist", func() {
			It("should return 404", func() {
				_, status := suite.Repository.CheckRepositoryOciImage(suite.Ctx, suite.OrgID, "nonexistent", "quay.io/myorg/myimage")
				Expect(status.Code).To(Equal(int32(http.StatusNotFound)))
			})
		})

		When("the repository is not of OCI type", func() {
			It("should return 400", func() {
				spec := api.RepositorySpec{}
				err := spec.FromGitRepoSpec(api.GitRepoSpec{
					Url:  "https://github.com/flightctl/flightctl.git",
					Type: api.GitRepoSpecTypeGit,
				})
				Expect(err).ToNot(HaveOccurred())
				_, status := suite.Repository.CreateRepository(suite.Ctx, suite.OrgID, api.Repository{
					ApiVersion: "v1beta1",
					Kind:       "Repository",
					Metadata:   api.ObjectMeta{Name: lo.ToPtr("git-repo-2")},
					Spec:       spec,
				})
				Expect(status.Code).To(Equal(int32(http.StatusCreated)))

				_, status = suite.Repository.CheckRepositoryOciImage(suite.Ctx, suite.OrgID, "git-repo-2", "quay.io/myorg/myimage")
				Expect(status.Code).To(Equal(int32(http.StatusBadRequest)))
				Expect(status.Message).To(ContainSubstring("not OCI"))
			})
		})

		When("the repository is OCI type and the registry is accessible", func() {
			It("should return 200 with accessible=true", func() {
				server := httptest.NewServer(mockOCIRegistryHandler())
				DeferCleanup(server.Close)

				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					Registry: registryHostFromURL(server.URL),
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Http),
				})
				Expect(err).ToNot(HaveOccurred())
				_, status := suite.Repository.CreateRepository(suite.Ctx, suite.OrgID, api.Repository{
					ApiVersion: "v1beta1",
					Kind:       "Repository",
					Metadata:   api.ObjectMeta{Name: lo.ToPtr("oci-repo")},
					Spec:       spec,
				})
				Expect(status.Code).To(Equal(int32(http.StatusCreated)))

				result, status := suite.Repository.CheckRepositoryOciImage(suite.Ctx, suite.OrgID, "oci-repo", "myimage")
				Expect(status.Code).To(Equal(int32(http.StatusOK)))
				Expect(result).ToNot(BeNil())
				Expect(result.Accessible).To(BeTrue())
			})
		})

		When("the registry is not reachable", func() {
			// A network-level failure (connection refused) is not an HTTP response,
			// so errorCode is 0 – callers must inspect errorMessage for details.
			It("should return 200 with accessible=false and errorCode 0", func() {
				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					// port 1 is effectively unreachable
					Registry: "127.0.0.1:1",
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Http),
				})
				Expect(err).ToNot(HaveOccurred())
				_, status := suite.Repository.CreateRepository(suite.Ctx, suite.OrgID, api.Repository{
					ApiVersion: "v1beta1",
					Kind:       "Repository",
					Metadata:   api.ObjectMeta{Name: lo.ToPtr("unreachable-oci-repo")},
					Spec:       spec,
				})
				Expect(status.Code).To(Equal(int32(http.StatusCreated)))

				result, status := suite.Repository.CheckRepositoryOciImage(suite.Ctx, suite.OrgID, "unreachable-oci-repo", "myimage")
				Expect(status.Code).To(Equal(int32(http.StatusOK)))
				Expect(result).ToNot(BeNil())
				Expect(result.Accessible).To(BeFalse())
				Expect(result.ErrorCode).To(Equal(0))
				Expect(result.ErrorMessage).ToNot(BeEmpty())
			})
		})
	})
})
