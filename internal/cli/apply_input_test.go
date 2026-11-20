package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/stretchr/testify/require"
)

func TestApplyNestedNumbers(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var device api.Device
		require.NoError(t, json.Unmarshal(body, &device))
		monitor, err := (*device.Spec.Resources)[0].AsCpuResourceMonitorSpec()
		require.NoError(t, err)
		require.Equal(t, float32(3.5), monitor.AlertRules[0].Percentage)
		require.Equal(t, float32(3), monitor.AlertRules[1].Percentage)
		var document genericResource
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&document))
		resources := document["spec"].(map[string]interface{})["resources"].([]interface{})
		rules := resources[0].(map[string]interface{})["alertRules"].([]interface{})
		require.Equal(t, json.Number("3.5"), rules[0].(map[string]interface{})["percentage"])
		require.Equal(t, json.Number("3"), rules[1].(map[string]interface{})["percentage"])
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{}`)
	}))
	defer server.Close()
	generated, err := apiclient.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	document := `{"apiVersion":"flightctl.io/v1beta1","kind":"Device","metadata":{"name":"device"},"spec":{"resources":[{"monitorType":"CPU","samplingInterval":"1s","alertRules":[{"percentage":3.5},{"percentage":30e-1}]}]}}`
	require.Empty(t, applyFromReader(t.Context(), client.NewTestClient(generated), nil, "device", strings.NewReader(document), false))
	require.Equal(t, 1, requests)
}

func TestApplyPolicyRejectsMalformedVersion(t *testing.T) {
	for _, version := range []string{`2`, `null`, `false`, `{}`, `[]`, `""`} {
		t.Run("When version is "+version+" it should reject before HTTP", func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				writer.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			generated, err := apiclient.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			document := `{"apiVersion":"flightctl.io/v1beta1","kind":"EnrollmentApprovalPolicy","metadata":{"name":"west","resourceVersion":` + version + `}}`
			failures := applyFromReader(t.Context(), client.NewTestClient(generated), nil, "policy", strings.NewReader(document), false)
			require.Len(t, failures, 1)
			require.ErrorContains(t, failures[0], "metadata.resourceVersion must be a non-empty string")
			require.Zero(t, requests)
		})
	}
}

func TestApplyPolicyPreservesStaleVersionConflict(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusConflict)
		if request.Method == http.MethodPost {
			_, _ = io.WriteString(writer, `{"code":409,"reason":"AlreadyExists"}`)
			return
		}
		var policy api.EnrollmentApprovalPolicy
		require.NoError(t, json.NewDecoder(request.Body).Decode(&policy))
		require.Equal(t, "2", *policy.Metadata.ResourceVersion)
		_, _ = io.WriteString(writer, `{"code":409,"reason":"ResourceVersionConflict","message":"stale version"}`)
	}))
	defer server.Close()
	generated, err := apiclient.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	document := `{"apiVersion":"flightctl.io/v1beta1","kind":"EnrollmentApprovalPolicy","metadata":{"name":"west","resourceVersion":"2"}}`
	failures := applyFromReader(t.Context(), client.NewTestClient(generated), nil, "policy", strings.NewReader(document), false)
	require.Len(t, failures, 1)
	require.ErrorContains(t, failures[0], "stale version")
	require.Equal(t, []string{"POST", "PUT"}, methods)
}

func TestApplyExactIntegralNumbers(t *testing.T) {
	for _, testCase := range []struct {
		number  string
		want    int64
		invalid bool
	}{
		{"3.0", 3, false}, {"3e0", 3, false}, {"30e-1", 3, false},
		{"3.00E+0", 3, false}, {"-0.0", 0, false},
		{"0e999999999999999999", 0, false},
		{"-0e-999999999999999999", 0, false},
		{"9007199254740993", 9007199254740993, false},
		{"9007199254740993.0", 9007199254740993, false},
		{"9.223372036854775807e18", 9223372036854775807, false},
		{"-9.223372036854775808e18", -9223372036854775808, false},
		{"9223372036854775807", 9223372036854775807, false},
		{"-9223372036854775808", -9223372036854775808, false},
		{"3.5", 0, true}, {"9223372036854775808", 0, true},
		{"1e2147483647", 0, true}, {"1e-2147483648", 0, true}, {"1e999999999999999999999", 0, true},
	} {
		t.Run("When JSON contains "+testCase.number+" it should preserve typed decoding", func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				var device api.Device
				err := json.NewDecoder(request.Body).Decode(&device)
				writer.Header().Set("Content-Type", "application/json")
				if testCase.invalid {
					require.Error(t, err)
					writer.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(writer, `{"message":"invalid numeric encoding"}`)
					return
				}
				require.NoError(t, err)
				require.Equal(t, testCase.want, *device.Spec.Firmware.RetryGeneration)
				_, _ = io.WriteString(writer, `{}`)
			}))
			defer server.Close()
			generated, err := apiclient.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			document := fmt.Sprintf(`{"apiVersion":"flightctl.io/v1beta1","kind":"Device","metadata":{"name":"device"},"spec":{"firmware":{"retryGeneration":%s}}}`, testCase.number)
			failures := applyFromReader(t.Context(), client.NewTestClient(generated), nil, "device", strings.NewReader(document), false)
			if testCase.invalid {
				require.NotEmpty(t, failures)
			} else {
				require.Empty(t, failures)
			}
			require.Equal(t, 1, requests)
		})
	}
	for _, number := range []string{"3.0", "3e0", "9223372036854775807"} {
		t.Run("When YAML contains "+number+" it should remain compatible", func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				var policy api.EnrollmentApprovalPolicy
				require.NoError(t, json.NewDecoder(request.Body).Decode(&policy))
				want := int64(3)
				if number == "9223372036854775807" {
					want = 9223372036854775807
				}
				require.Equal(t, want, *policy.Spec.ApprovalLimit)
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(writer, `{}`)
			}))
			defer server.Close()
			generated, err := apiclient.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			document := "apiVersion: flightctl.io/v1beta1\nkind: EnrollmentApprovalPolicy\nmetadata:\n  name: west\nspec:\n  approvalLimit: " + number + "\n"
			require.Empty(t, applyFromReader(t.Context(), client.NewTestClient(generated), nil, "policy", strings.NewReader(document), false))
			require.Equal(t, 1, requests)
		})
	}
}
