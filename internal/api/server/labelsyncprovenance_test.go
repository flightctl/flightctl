package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/getkin/kin-openapi/openapi3filter"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type labelSyncProvenanceTransportStub struct {
	Unimplemented
	called    int
	labelKeys []string
}

func (s *labelSyncProvenanceTransportStub) GetLabelSyncProvenance(w http.ResponseWriter, _ *http.Request, params apiv1beta1.GetLabelSyncProvenanceParams) {
	s.called++
	s.labelKeys = params.LabelKeys
	w.WriteHeader(http.StatusNoContent)
}

func TestLabelSyncProvenanceRouteValidationAndRBAC(t *testing.T) {
	t.Run("When repeated keys are within the limit it should pass them to the transport", func(t *testing.T) {
		spec, err := apiv1beta1.GetSpec()
		require.NoError(t, err)
		transportStub := &labelSyncProvenanceTransportStub{}
		router := HandlerWithOptions(transportStub, ChiServerOptions{
			BaseURL: "/api/v1",
			Middlewares: []MiddlewareFunc{
				oapimiddleware.OapiRequestValidatorWithOptions(spec, &oapimiddleware.Options{
					Options:               openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
					SilenceServersWarning: true,
				}),
			},
		})
		request := httptest.NewRequest(http.MethodGet, "/api/v1/labelsyncprovenance?labelKeys=hardware.architecture&labelKeys=operator.label", nil)
		response := httptest.NewRecorder()

		router.ServeHTTP(response, request)

		require.Equal(t, http.StatusNoContent, response.Code)
		assert.Equal(t, []string{"hardware.architecture", "operator.label"}, transportStub.labelKeys)
		assert.Equal(t, 1, transportStub.called)
	})

	t.Run("When more than 50 keys are supplied it should reject before reaching the transport", func(t *testing.T) {
		spec, err := apiv1beta1.GetSpec()
		require.NoError(t, err)
		transportStub := &labelSyncProvenanceTransportStub{}
		router := HandlerWithOptions(transportStub, ChiServerOptions{
			BaseURL: "/api/v1",
			Middlewares: []MiddlewareFunc{
				oapimiddleware.OapiRequestValidatorWithOptions(spec, &oapimiddleware.Options{
					Options:               openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
					SilenceServersWarning: true,
				}),
			},
		})
		query := url.Values{}
		for i := 0; i < 51; i++ {
			query.Add("labelKeys", fmt.Sprintf("key-%d", i))
		}
		request := httptest.NewRequest(http.MethodGet, "/api/v1/labelsyncprovenance?"+query.Encode(), nil)
		response := httptest.NewRecorder()

		router.ServeHTTP(response, request)

		assert.Equal(t, http.StatusBadRequest, response.Code)
		assert.Zero(t, transportStub.called)
	})

	t.Run("When provenance routes are registered it should use existing read permissions", func(t *testing.T) {
		deviceEndpoint := APIMetadataMap["GET:/devices/{name}/labelsyncprovenance"]
		orgEndpoint := APIMetadataMap["GET:/labelsyncprovenance"]

		require.NotNil(t, deviceEndpoint)
		require.NotNil(t, orgEndpoint)
		assert.Equal(t, "devices", deviceEndpoint.Resource)
		assert.Equal(t, "get", deviceEndpoint.Action)
		assert.Equal(t, "labelsyncmappings", orgEndpoint.Resource)
		assert.Equal(t, "list", orgEndpoint.Action)
	})
}
