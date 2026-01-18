package versioning_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flightctl/flightctl/internal/api_server/versioning"
	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestVersioningIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Versioning Integration Suite")
}

var _ = Describe("API Version Negotiation HTTP", func() {
	var svr *httptest.Server

	BeforeEach(func() {
		registry := versioning.NewRegistry(versioning.V1Beta1)

		v1beta1Router := chi.NewRouter()
		v1beta1Router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		dispatcher := versioning.NewDispatcher(registry, map[versioning.Version]chi.Router{
			versioning.V1Beta1: v1beta1Router,
		})

		router := chi.NewRouter()
		router.Route("/api/v1", func(r chi.Router) {
			r.Use(versioning.Middleware(registry))
			r.Mount("/", dispatcher)
		})

		svr = httptest.NewServer(router)
	})

	AfterEach(func() {
		svr.Close()
	})

	It("returns version header with fallback version", func() {
		resp, err := http.Get(svr.URL + "/api/v1/devices")
		Expect(err).ToNot(HaveOccurred())
		defer resp.Body.Close()

		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Header.Get(versioning.HeaderAPIVersion)).To(Equal(string(versioning.V1Beta1)))
	})

	It("returns Vary header for cache differentiation", func() {
		resp, err := http.Get(svr.URL + "/api/v1/devices")
		Expect(err).ToNot(HaveOccurred())
		defer resp.Body.Close()

		Expect(resp.Header.Get("Vary")).To(Equal(versioning.HeaderAPIVersion))
	})

	It("returns 406 when unsupported version requested", func() {
		req, _ := http.NewRequest(http.MethodGet, svr.URL+"/api/v1/devices", nil)
		req.Header.Set(versioning.HeaderAPIVersion, "v999")

		resp, err := http.DefaultClient.Do(req)
		Expect(err).ToNot(HaveOccurred())
		defer resp.Body.Close()

		Expect(resp.StatusCode).To(Equal(http.StatusNotAcceptable))
		Expect(resp.Header.Get(versioning.HeaderAPIVersion)).To(Equal(string(versioning.V1Beta1)))
	})
})
