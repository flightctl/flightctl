package middleware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	orgmodel "github.com/flightctl/flightctl/internal/org/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestRequestSizeLimiter(t *testing.T) {
	require := require.New(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	testCases := []struct {
		Name               string
		URL                string
		Headers            map[string]string
		MaxURLLength       int
		MaxNumHeaders      int
		ExpectedStatusCode int
		ExpectedErrorMsg   string
	}{
		{
			Name:               "Valid request",
			URL:                "/api/v1/devices",
			MaxURLLength:       100,
			MaxNumHeaders:      10,
			ExpectedStatusCode: http.StatusOK,
		},
		{
			Name:               "URL too long",
			URL:                "/api/v1/devices?param1=reallylongparametervalue",
			MaxURLLength:       20,
			MaxNumHeaders:      10,
			ExpectedStatusCode: http.StatusRequestURITooLong,
			ExpectedErrorMsg:   "URL too long, exceeds 20 characters",
		},
		{
			Name: "Too many headers",
			Headers: map[string]string{
				"header1": "value1",
				"header2": "value2",
				"header3": "value3",
			},
			MaxURLLength:       100,
			MaxNumHeaders:      2,
			ExpectedStatusCode: http.StatusRequestHeaderFieldsTooLarge,
			ExpectedErrorMsg:   "Request has too many headers, exceeds 2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.URL, nil)
			for k, v := range tc.Headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()

			limiter := middleware.RequestSizeLimiter(tc.MaxURLLength, tc.MaxNumHeaders)
			limiter(handler).ServeHTTP(rr, req)

			require.Equal(tc.ExpectedStatusCode, rr.Code)
			if tc.ExpectedErrorMsg != "" {
				var status api.Status
				err := json.Unmarshal(rr.Body.Bytes(), &status)
				require.NoError(err)
				require.Equal(tc.ExpectedErrorMsg, status.Message)
				require.Equal("application/json", rr.Header().Get("Content-Type"))
			}
		})
	}
}

func TestExtractAndValidateOrg(t *testing.T) {
	require := require.New(t)
	log := logrus.New()
	org1 := uuid.New()
	org2 := uuid.New()

	testCases := []struct {
		Name               string
		Extractor          middleware.OrgIDExtractor
		Identity           *identity.MappedIdentity
		ExpectedStatusCode int
		ExpectedErrorMsg   string
	}{
		{
			Name: "No mapped identity",
			Extractor: func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
				return uuid.Nil, false, nil
			},
			Identity:           nil,
			ExpectedStatusCode: http.StatusInternalServerError,
			ExpectedErrorMsg:   flterrors.ErrNoMappedIdentity.Error(),
		},
		{
			Name: "Not an organization member",
			Extractor: func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
				return org2, true, nil
			},
			Identity:           identity.NewMappedIdentity("user", "", []*orgmodel.Organization{{ID: org1}}, nil, false, nil),
			ExpectedStatusCode: http.StatusForbidden,
			ExpectedErrorMsg:   flterrors.ErrNotOrgMember.Error(),
		},
		{
			Name: "Ambiguous organization",
			Extractor: func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
				return uuid.Nil, false, nil
			},
			Identity:           identity.NewMappedIdentity("user", "", []*orgmodel.Organization{{ID: org1}, {ID: org2}}, nil, false, nil),
			ExpectedStatusCode: http.StatusBadRequest,
			ExpectedErrorMsg:   flterrors.ErrAmbiguousOrganization.Error(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.Identity != nil {
				ctx := context.WithValue(req.Context(), consts.MappedIdentityCtxKey, tc.Identity)
				req = req.WithContext(ctx)
			}
			rr := httptest.NewRecorder()

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			middleware.ExtractAndValidateOrg(tc.Extractor, log)(handler).ServeHTTP(rr, req)

			require.Equal(tc.ExpectedStatusCode, rr.Code)
			if tc.ExpectedErrorMsg != "" {
				var status api.Status
				err := json.Unmarshal(rr.Body.Bytes(), &status)
				require.NoError(err)
				require.Equal(tc.ExpectedErrorMsg, status.Message)
				require.Equal("application/json", rr.Header().Get("Content-Type"))
			}
		})
	}
}