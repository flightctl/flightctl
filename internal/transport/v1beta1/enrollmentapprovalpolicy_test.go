package transportv1beta1

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApprovalPolicyPatchPreservesInt64Limit(t *testing.T) {
	controller := gomock.NewController(t)
	permissions := auth.NewMockAuthZMiddleware(controller)
	permissions.EXPECT().CheckPermission(gomock.Any(), "enrollmentapprovalpolicies/approvallimit", "update").Return(true, nil)
	service := policyservice.NewMockService(controller)
	service.EXPECT().PatchEnrollmentApprovalPolicy(gomock.Any(), gomock.Any(), "west", gomock.Any()).Do(func(_ any, _ any, _ any, patch domain.PatchRequest) {
		require.Equal(t, json.Number("9223372036854775807"), patch[0].Value)
	}).Return(nil, domain.StatusOK())
	handler := &TransportHandler{converter: convert.NewConverter(), authZ: permissions, enrollmentapprovalpolicy: service}
	response := httptest.NewRecorder()
	handler.PatchEnrollmentApprovalPolicy(response, httptest.NewRequest(http.MethodPatch, "/enrollmentapprovalpolicies/west", strings.NewReader(`[{"op":"add","path":"/spec/approvalLimit","value":9223372036854775807}]`)), "west")
	require.Equal(t, http.StatusOK, response.Code)
}

func TestApprovalPolicyRejectsUntrustedStatus(t *testing.T) {
	for _, body := range []string{`{"status":null}`, `{"status":{"enrollmentCount":0}}`, `{"spec":{"unknown":true}}`, `{} {}`} {
		t.Run(body, func(t *testing.T) {
			handler := &TransportHandler{converter: convert.NewConverter(), enrollmentapprovalpolicy: policyservice.NewMockService(gomock.NewController(t))}
			response := httptest.NewRecorder()
			handler.CreateEnrollmentApprovalPolicy(response, httptest.NewRequest(http.MethodPost, "/enrollmentapprovalpolicies", strings.NewReader(body)))
			require.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}

func TestApprovalPolicyPatchRejectsTrailingDocument(t *testing.T) {
	handler := &TransportHandler{converter: convert.NewConverter(), enrollmentapprovalpolicy: policyservice.NewMockService(gomock.NewController(t))}
	response := httptest.NewRecorder()
	handler.PatchEnrollmentApprovalPolicy(response, httptest.NewRequest(http.MethodPatch, "/enrollmentapprovalpolicies/west", strings.NewReader(`[] {}`)), "west")
	require.Equal(t, http.StatusBadRequest, response.Code)
}

func TestApprovalPolicyQuotaAuthorizationError(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		code int
	}{
		{"unavailable", errors.New("authorization backend unavailable"), http.StatusServiceUnavailable},
		{"invalid claims", flterrors.ErrInvalidTokenClaims, http.StatusBadRequest},
		{"denied", nil, http.StatusForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			permissions := auth.NewMockAuthZMiddleware(gomock.NewController(t))
			permissions.EXPECT().CheckPermission(gomock.Any(), "enrollmentapprovalpolicies/approvallimit", "update").Return(false, testCase.err)
			handler := &TransportHandler{converter: convert.NewConverter(), authZ: permissions}
			response := httptest.NewRecorder()
			require.False(t, handler.quotaAllowed(response, httptest.NewRequest(http.MethodPost, "/", nil)))
			require.Equal(t, testCase.code, response.Code)
		})
	}
}

func TestApprovalPolicyQuotaTestDoesNotRequireQuotaWritePermission(t *testing.T) {
	controller := gomock.NewController(t)
	service := policyservice.NewMockService(controller)
	service.EXPECT().PatchEnrollmentApprovalPolicy(gomock.Any(), gomock.Any(), "west", gomock.Any()).Return(nil, domain.StatusOK())
	handler := &TransportHandler{converter: convert.NewConverter(), authZ: auth.NewMockAuthZMiddleware(controller), enrollmentapprovalpolicy: service}
	response := httptest.NewRecorder()
	body := `[{"op":"test","path":"/metadata/resourceVersion","value":"1"},{"op":"test","path":"/spec/approvalLimit","value":10}]`
	handler.PatchEnrollmentApprovalPolicy(response, httptest.NewRequest(http.MethodPatch, "/enrollmentapprovalpolicies/west", strings.NewReader(body)), "west")
	require.Equal(t, http.StatusOK, response.Code)
}

func TestApprovalPolicyDecodeErrorReasons(t *testing.T) {
	for _, testCase := range []struct {
		body   string
		reason string
	}{
		{`{"status":null}`, "ProtectedField"},
		{`{"spec":{"unknown":true}}`, "Bad Request"},
		{`{} {}`, "Bad Request"},
	} {
		t.Run(testCase.body, func(t *testing.T) {
			handler := &TransportHandler{converter: convert.NewConverter()}
			response := httptest.NewRecorder()
			handler.CreateEnrollmentApprovalPolicy(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(testCase.body)))
			require.Equal(t, http.StatusBadRequest, response.Code)
			var status domain.Status
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &status))
			require.Equal(t, testCase.reason, status.Reason)
		})
	}
}
