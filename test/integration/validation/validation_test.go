package validation

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/versioning"
	"github.com/go-chi/chi/v5"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestValidation(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "API Validation Suite")
}

var _ = Describe("API Validation Tests", func() {
	var (
		svr *httptest.Server
	)

	BeforeEach(func() {
		// Create a minimal test server with OpenAPI validation middleware
		// This simulates the real API server's validation behavior

		// Load the OpenAPI spec
		v1beta1Swagger, err := corev1beta1.GetSwagger()
		Expect(err).ToNot(HaveOccurred())

		// Create validation middleware with same config as real server
		v1beta1OapiMiddleware := oapimiddleware.OapiRequestValidatorWithOptions(v1beta1Swagger, &oapimiddleware.Options{
			ErrorHandler:          apiserver.OapiErrorHandlerForVersion(versioning.V1Beta1),
			MultiErrorHandler:     apiserver.OapiMultiErrorHandler,
			SilenceServersWarning: true,
		})

		// Create a simple router that just validates requests and returns 200
		router := chi.NewRouter()
		router.Use(v1beta1OapiMiddleware)

		// Add minimal handlers that just return success if validation passes
		router.Post("/api/v1/fleets", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			// Return minimal fleet response
			_, _ = w.Write([]byte(`{"apiVersion":"flightctl.io/v1beta1","kind":"Fleet","metadata":{"name":"test"}}`))
		})

		router.Post("/api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"apiVersion":"flightctl.io/v1beta1","kind":"Device","metadata":{"name":"test"}}`))
		})

		svr = httptest.NewServer(router)
	})

	AfterEach(func() {
		svr.Close()
	})

	Context("Fleet validation", func() {
		It("should reject Fleet creation with unknown properties", func() {
			// Create a Fleet with an unknown field
			fleetWithUnknownProperty := map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Fleet",
				"metadata": map[string]interface{}{
					"name": "validation-test-fleet",
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{},
					},
				},
				// This unknown field should cause validation to fail
				"unknownField": "this-should-be-rejected",
			}

			// Marshal to JSON
			fleetJSON, err := json.Marshal(fleetWithUnknownProperty)
			Expect(err).ToNot(HaveOccurred())

			// Send the request to the test server
			url := svr.URL + "/api/v1/fleets"
			resp, err := http.Post(url, "application/json", bytes.NewReader(fleetJSON)) // #nosec G107
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			// We expect the request to fail with 400 Bad Request due to the unknown field
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))

			// Check that the error message mentions the unknown property
			body, err := io.ReadAll(resp.Body)
			Expect(err).ToNot(HaveOccurred())
			bodyStr := strings.ToLower(string(body))

			// The error should mention the unknown field in some way
			Expect(bodyStr).To(Or(
				ContainSubstring("unknown"),
				ContainSubstring("additional"),
				ContainSubstring("not allowed"),
				ContainSubstring("extra"),
				ContainSubstring("unknownfield"),
				ContainSubstring("unsupported"),
			))
		})

		It("should reject Fleet spec with unknown properties", func() {
			// Create a Fleet with an unknown field in the spec
			fleetWithUnknownSpecProperty := map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Fleet",
				"metadata": map[string]interface{}{
					"name": "validation-test-fleet",
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{},
					},
					// This unknown field in spec should cause validation to fail
					"invalidSpecField": "should-be-rejected",
				},
			}

			// Marshal to JSON
			fleetJSON, err := json.Marshal(fleetWithUnknownSpecProperty)
			Expect(err).ToNot(HaveOccurred())

			// Send the request to the test server
			url := svr.URL + "/api/v1/fleets"
			resp, err := http.Post(url, "application/json", bytes.NewReader(fleetJSON)) // #nosec G107
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			// We expect the request to fail with 400 Bad Request due to the unknown field
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))

			// Check that the error message mentions the unknown property
			body, err := io.ReadAll(resp.Body)
			Expect(err).ToNot(HaveOccurred())
			bodyStr := strings.ToLower(string(body))

			// The error should mention the unknown field in some way
			Expect(bodyStr).To(Or(
				ContainSubstring("unknown"),
				ContainSubstring("additional"),
				ContainSubstring("not allowed"),
				ContainSubstring("extra"),
				ContainSubstring("invalidspecfield"),
				ContainSubstring("unsupported"),
			))
		})

		It("should accept Fleet creation with valid properties", func() {
			// Create a valid Fleet without unknown fields
			validFleet := map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Fleet",
				"metadata": map[string]interface{}{
					"name": "validation-test-valid-fleet",
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{},
					},
				},
			}

			// Marshal to JSON
			fleetJSON, err := json.Marshal(validFleet)
			Expect(err).ToNot(HaveOccurred())

			// Send the request to the test server
			url := svr.URL + "/api/v1/fleets"
			resp, err := http.Post(url, "application/json", bytes.NewReader(fleetJSON)) // #nosec G107
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			// We expect this to succeed (or at least pass validation)
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
		})
	})

	Context("Device validation", func() {
		It("should reject Device creation with unknown properties", func() {
			// Create a Device with an unknown field
			deviceWithUnknownProperty := map[string]interface{}{
				"apiVersion": "flightctl.io/v1beta1",
				"kind":       "Device",
				"metadata": map[string]interface{}{
					"name": "validation-test-device",
				},
				"spec": map[string]interface{}{},
				// This unknown field should cause validation to fail
				"invalidProperty": "should-be-rejected",
			}

			// Marshal to JSON
			deviceJSON, err := json.Marshal(deviceWithUnknownProperty)
			Expect(err).ToNot(HaveOccurred())

			// Send the request to the test server
			url := svr.URL + "/api/v1/devices"
			resp, err := http.Post(url, "application/json", bytes.NewReader(deviceJSON)) // #nosec G107
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			// We expect the request to fail with 400 Bad Request due to the unknown field
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))

			// Check that the error message mentions the unknown property
			body, err := io.ReadAll(resp.Body)
			Expect(err).ToNot(HaveOccurred())
			bodyStr := strings.ToLower(string(body))

			// The error should mention the unknown field
			Expect(bodyStr).To(Or(
				ContainSubstring("unknown"),
				ContainSubstring("additional"),
				ContainSubstring("not allowed"),
				ContainSubstring("extra"),
				ContainSubstring("invalidproperty"),
				ContainSubstring("unsupported"),
			))
		})

		// Note: DeviceSpec cannot have strict validation (additionalProperties: false)
		// because it's used in allOf compositions (e.g., TemplateVersionStatus) where
		// additionalProperties: false would prevent the composition from working properly.
		// Therefore, we don't test for unknown properties in Device spec.
	})
})
