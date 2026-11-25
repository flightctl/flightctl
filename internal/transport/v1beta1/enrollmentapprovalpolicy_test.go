package transportv1beta1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/domain"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApprovalPolicyPatchPreservesJSONNumbers(t *testing.T) {
	controller := gomock.NewController(t)
	permissions := auth.NewMockAuthZMiddleware(controller)
	service := policyservice.NewMockService(controller)
	service.EXPECT().PatchEnrollmentApprovalPolicy(gomock.Any(), gomock.Any(), "west", gomock.Any()).Do(func(_ any, _ any, _ any, patch domain.PatchRequest) {
		require.Equal(t, json.Number("9223372036854775807"), patch[0].Value)
	}).Return(nil, domain.StatusOK())
	handler := &TransportHandler{converter: convert.NewConverter(), authZ: permissions, enrollmentapprovalpolicy: service}
	response := httptest.NewRecorder()
	handler.PatchEnrollmentApprovalPolicy(response, httptest.NewRequest(http.MethodPatch, "/enrollmentapprovalpolicies/west", strings.NewReader(`[{"op":"add","path":"/spec/approvalLimit","value":9223372036854775807}]`)), "west")
	require.Equal(t, http.StatusOK, response.Code)
}

func TestApprovalPolicyPatchValuePresence(t *testing.T) {
	for _, testCase := range []struct {
		name, body string
		valid      bool
	}{
		{"When replacing with null it should preserve an explicit value", `[{"op":"replace","path":"/spec/autoApprovalWindowDays","value":null}]`, true},
		{"When testing null it should preserve an explicit value", `[{"op":"test","path":"/spec/autoApprovalWindowDays","value":null}]`, true},
		{"When replace omits value it should reject", `[{"op":"replace","path":"/spec/autoApprovalWindowDays"}]`, false},
		{"When add omits value it should reject", `[{"op":"add","path":"/spec/autoApprovalWindowDays"}]`, false},
		{"When test omits value it should reject", `[{"op":"test","path":"/spec/autoApprovalWindowDays"}]`, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			controller := gomock.NewController(t)
			service := policyservice.NewMockService(controller)
			if testCase.valid {
				service.EXPECT().PatchEnrollmentApprovalPolicy(gomock.Any(), gomock.Any(), "west", gomock.Any()).Do(func(_ any, _ any, _ any, patch domain.PatchRequest) {
					encoded, err := json.Marshal(patch)
					require.NoError(t, err)
					require.JSONEq(t, testCase.body, string(encoded))
				}).Return(nil, domain.StatusOK())
			}
			handler := &TransportHandler{converter: convert.NewConverter(), authZ: auth.NewMockAuthZMiddleware(controller), enrollmentapprovalpolicy: service}
			response := httptest.NewRecorder()
			handler.PatchEnrollmentApprovalPolicy(response, httptest.NewRequest(http.MethodPatch, "/enrollmentapprovalpolicies/west", strings.NewReader(testCase.body)), "west")
			if testCase.valid {
				require.Equal(t, http.StatusOK, response.Code)
			} else {
				require.Equal(t, http.StatusBadRequest, response.Code)
				var status domain.Status
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &status))
				require.Equal(t, "Bad Request", status.Reason)
			}
		})
	}
}

func TestApprovalPolicyRejectsUntrustedStatus(t *testing.T) {
	for _, body := range []string{`{"status":null}`, `{"status":{"enrollmentCount":0}}`, `{"spec":{"unknown":true}}`, `{} {}`} {
		t.Run("When the body is "+body+" it should reject untrusted input", func(t *testing.T) {
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

func TestApprovalPolicyTrailingSyntaxCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		decode func() error
	}{
		{"When a policy document has invalid trailing JSON it should retain the syntax cause", func() error {
			return decodePolicy(strings.NewReader(`{} !`), new(map[string]any))
		}},
		{"When a patch document has invalid trailing JSON it should retain the syntax cause", func() error {
			_, err := decodePolicyPatch(strings.NewReader(`[] !`))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var syntax *json.SyntaxError
			require.ErrorAs(t, tc.decode(), &syntax)
		})
	}
}

func TestApprovalPolicyLimitTestUsesPolicyPermission(t *testing.T) {
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
		t.Run("When decoding "+testCase.body+" it should distinguish the error reason", func(t *testing.T) {
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
