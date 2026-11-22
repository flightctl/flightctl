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

func TestApplyPolicyLimitInputs(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		for _, tc := range []struct {
			value   string
			invalid bool
		}{
			{"", false}, {"null", false}, {"0", false}, {"2147483647", false},
			{"-1", true}, {"2147483648", true}, {"0.5", true}, {`"bad"`, true},
		} {
			t.Run("When "+format+" has limit "+tc.value+" it should enforce the policy range", func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					var policy api.EnrollmentApprovalPolicy
					err := json.NewDecoder(r.Body).Decode(&policy)
					w.Header().Set("Content-Type", "application/json")
					if tc.invalid {
						require.True(t, err != nil || len(policy.Validate()) > 0)
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"message":"invalid policy limit"}`)
						return
					}
					require.NoError(t, err)
					require.Empty(t, policy.Validate())
					if tc.value == "" || tc.value == "null" {
						require.Nil(t, policy.Spec.ApprovalLimit)
					} else {
						require.Equal(t, tc.value, fmt.Sprint(*policy.Spec.ApprovalLimit))
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{}`)
				}))
				defer server.Close()
				generated, err := apiclient.NewClientWithResponses(server.URL)
				require.NoError(t, err)
				limit := ""
				if tc.value != "" {
					limit = `,"approvalLimit":` + tc.value
				}
				document := `{"apiVersion":"flightctl.io/v1beta1","kind":"EnrollmentApprovalPolicy","metadata":{"name":"west"},"spec":{"certificateName":"credential","matchExpression":"true","deviceLabels":{}` + limit + `}}`
				if format == "yaml" {
					document = "apiVersion: flightctl.io/v1beta1\nkind: EnrollmentApprovalPolicy\nmetadata:\n  name: west\nspec:\n  certificateName: credential\n  matchExpression: 'true'\n  deviceLabels: {}\n"
					if tc.value != "" {
						document += "  approvalLimit: " + tc.value + "\n"
					}
				}
				failures := applyFromReader(t.Context(), client.NewTestClient(generated), nil, "policy", strings.NewReader(document), false)
				if tc.invalid {
					require.NotEmpty(t, failures)
				} else {
					require.Empty(t, failures)
				}
				require.Equal(t, 1, requests)
			})
		}
	}
}
