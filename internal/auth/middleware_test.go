package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func createAuthZMiddleware(authZ AuthZMiddleware) http.Handler {
	return CreateAuthZMiddleware(authZ, logrus.New())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func createAuthNMiddleware(authN common.AuthNMiddleware) http.Handler {
	return CreateAuthNMiddleware(authN, logrus.New())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

type TestRequest struct {
	url        string
	method     string
	resource   string
	op         string
	badRequest bool
}

var requests []TestRequest = []TestRequest{
	{
		url:      "https://fctl.io/api/v1/devices",
		method:   http.MethodGet,
		resource: "devices",
		op:       "list",
	},
	{
		url:      "https://fctl.io/api/v1/devices",
		method:   http.MethodPost,
		resource: "devices",
		op:       "create",
	},
	{
		url:      "https://fctl.io/api/v1/devices",
		method:   http.MethodDelete,
		resource: "devices",
		op:       "deletecollection",
	},
	{
		url:      "https://fctl.io/api/v1/devices",
		method:   http.MethodPatch,
		resource: "devices",
		op:       "patch",
	},
	{
		url:      "https://fctl.io/api/v1/devices",
		method:   http.MethodPut,
		resource: "devices",
		op:       "update",
	},
	{
		url:      "https://fctl.io/api/v1/devices/foo",
		method:   http.MethodGet,
		resource: "devices",
		op:       "get",
	},
	{
		url:      "https://fctl.io/api/v1/devices/foo",
		method:   http.MethodPatch,
		resource: "devices",
		op:       "patch",
	},
	{
		url:      "https://fctl.io/api/v1/devices/foo/status",
		method:   http.MethodGet,
		resource: "devices/status",
		op:       "get",
	},
	{
		url:      "https://fctl.io/api/v1/devices/foo/lastseen",
		method:   http.MethodGet,
		resource: "devices/lastseen",
		op:       "get",
	},
	{
		url:      "wss://fctl.io/ws/v1/devices/foo/console",
		method:   http.MethodGet,
		resource: "devices/console",
		op:       "get",
	},
	{
		url:      "https://fctl.io/api/v1/fleets/foo/templateVersions/bar",
		method:   http.MethodGet,
		resource: "fleets/templateversions",
		op:       "get",
	},
	{
		url:      "https://fctl.io/api/version",
		method:   http.MethodGet,
		resource: "version",
		op:       "get",
	},
	{
		url:      "https://fctl.io/api/version",
		method:   http.MethodPost,
		resource: "version",
		op:       "create",
	},
}

func TestPermissionCheck(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name    string
		allowed bool
		err     error
		expCode int
	}{
		{"allowed", true, nil, http.StatusOK},
		{"denied", false, nil, http.StatusForbidden},
		{"error", false, fmt.Errorf("auth error"), http.StatusServiceUnavailable},
	}

	for _, r := range requests {
		for _, tc := range testCases {
			authZMock := NewMockAuthZMiddleware(ctrl)
			authZMock.EXPECT().CheckPermission(gomock.Any(), r.resource, r.op).Return(tc.allowed, tc.err).Times(1)
			authZMiddleware := createAuthZMiddleware(authZMock)

			req := httptest.NewRequest(r.method, r.url, nil)
			w := httptest.NewRecorder()
			authZMiddleware.ServeHTTP(w, req)
			require.Equal(tc.expCode, w.Code)
		}
	}
}

// no permissions check is done
var noCheckRequests []TestRequest = []TestRequest{
	{
		url: "https://fctl.io/api",
	},
	{
		url: "https://fctl.io/api/v1",
	},
	{
		url: "https://fctl.io/api/v1/auth",
	},
	{
		url: "https://fctl.io/api/v1/auth/foo",
	},
	{
		url: "https://fctl.io/api/v1/auth/foo/bar",
	},
	{
		url: "https://fctl.io/api/v5",
	},
	{
		url: "wss://fctl.io/api/v5",
	},
	{
		url:        "https://fctl.io/api/foo",
		badRequest: true,
	},
	{
		url:        "wss://fctl.io/api/foo",
		badRequest: true,
	},
}

func TestNoPermissionCheck(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	for _, r := range noCheckRequests {
		authZMock := NewMockAuthZMiddleware(ctrl)
		authZMock.EXPECT().CheckPermission(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
		authZMiddleware := createAuthZMiddleware(authZMock)

		req := httptest.NewRequest(r.method, r.url, nil)
		w := httptest.NewRecorder()
		authZMiddleware.ServeHTTP(w, req)

		expectedCode := http.StatusOK
		if r.badRequest {
			expectedCode = http.StatusBadRequest
		}
		require.Equal(expectedCode, w.Code)
	}
}

var unsupportedMethods []string = []string{http.MethodOptions, http.MethodHead, http.MethodTrace}

func TestUnsupportedMethodCheck(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	for _, m := range unsupportedMethods {
		authZMock := NewMockAuthZMiddleware(ctrl)
		authZMock.EXPECT().CheckPermission(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
		authZMiddleware := createAuthZMiddleware(authZMock)

		req := httptest.NewRequest(m, "https://fctl.io/api/v1/devices", nil)
		w := httptest.NewRecorder()
		authZMiddleware.ServeHTTP(w, req)

		require.Equal(http.StatusBadRequest, w.Code)
	}
}

type AuthNTestRequest struct {
	url             string
	shouldSkipCheck bool
}

var authNRequests []AuthNTestRequest = []AuthNTestRequest{
	{
		url:             "https://fctl.io/api/v1/auth/config",
		shouldSkipCheck: true,
	},
	{
		url: "https://fctl.io/api/v1/auth/validate",
	},
	{
		url: "https://fctl.io/api/v1/foo",
	},
	{
		url: "https://fctl.io/api/v1/foo/bar",
	},
}

func TestAuthN(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		name        string
		withToken   bool
		tokenErr    error
		identityErr error
		expCode     int
	}{
		{"valid", true, nil, nil, http.StatusOK},
		{"invalid", true, fmt.Errorf("token invalid"), nil, http.StatusUnauthorized},
		{"tokenErr", true, fmt.Errorf("auth error"), nil, http.StatusUnauthorized},
		{"identityErr", true, nil, fmt.Errorf("auth error"), http.StatusOK},
		{"noToken", false, nil, nil, http.StatusBadRequest},
	}

	for _, r := range authNRequests {
		for _, tc := range testCases {
			authNMock := NewMockAuthNMiddleware(ctrl)

			if r.shouldSkipCheck || !tc.withToken {
				authNMock.EXPECT().ValidateToken(gomock.Any(), gomock.Any()).Times(0)
				if r.shouldSkipCheck {
					authNMock.EXPECT().GetAuthToken(gomock.Any()).Times(0)
				} else {
					authNMock.EXPECT().GetAuthToken(gomock.Any()).Times(1).Return("", fmt.Errorf("err"))
				}
			} else {
				authNMock.EXPECT().ValidateToken(gomock.Any(), gomock.Any()).Return(tc.tokenErr).Times(1)
				authNMock.EXPECT().GetAuthToken(gomock.Any()).Times(1).Return("token", nil)
				if tc.tokenErr != nil {
					authNMock.EXPECT().GetIdentity(gomock.Any(), gomock.Any()).Times(0)
				} else {
					authNMock.EXPECT().GetIdentity(gomock.Any(), "token").Return(&common.BaseIdentity{}, tc.identityErr).Times(1)
				}
			}
			authNMiddleware := createAuthNMiddleware(authNMock)

			req := httptest.NewRequest(http.MethodGet, r.url, nil)
			if tc.withToken {
				req.Header.Add(common.AuthHeader, "Bearer token")
			}
			w := httptest.NewRecorder()
			authNMiddleware.ServeHTTP(w, req)

			expectedCode := tc.expCode
			if r.shouldSkipCheck {
				expectedCode = http.StatusOK
			}
			require.Equal(expectedCode, w.Code)
		}
	}
}
