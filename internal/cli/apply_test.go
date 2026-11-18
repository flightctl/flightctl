package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestApplyRepositoryDevicePullAccess(t *testing.T) {
	for _, flag := range []*bool{nil, lo.ToPtr(false), lo.ToPtr(true)} {
		state, err := json.Marshal(flag)
		require.NoError(t, err)
		t.Run("When delivery is "+string(state)+" it should round trip get YAML through apply", func(t *testing.T) {
			repo := repositoryWithDelivery(t, flag)
			input := getRepositoryYAML(t, repo)
			c, recorder := newRecordingClient(t, http.StatusOK, repo)
			require.Empty(t, applyFromReader(t.Context(), c, nil, "repository.yaml", strings.NewReader(input), false))
			require.Len(t, recorder.requests, 1)
			require.Equal(t, http.MethodPut, recorder.requests[0].Method)
			require.Equal(t, "/repositories/registry", recorder.requests[0].URL.Path)
			var result api.Repository
			require.NoError(t, json.Unmarshal([]byte(recorder.bodies[0]), &result))
			require.Equal(t, repo.Spec, result.Spec)
		})
	}
}

func TestApplyApprovalPolicyPreservesApprovalLimit(t *testing.T) {
	for _, format := range []string{"direct", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var received int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body api.EnrollmentApprovalPolicy
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				require.NotNil(t, body.Spec.ApprovalLimit)
				received = *body.Spec.ApprovalLimit
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusCreated)
				_, _ = writer.Write([]byte(`{}`))
			}))
			defer server.Close()
			generated, err := apiclient.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			testClient := client.NewTestClient(generated)
			document := `{"apiVersion":"flightctl.io/v1beta1","kind":"EnrollmentApprovalPolicy","metadata":{"name":"west"},"spec":{"approvalLimit":9223372036854775807}}`
			if format == "direct" {
				result := applyResourceByKind(t.Context(), testClient, nil, EnrollmentApprovalPolicyKind, "west", []byte(document))
				require.NoError(t, result.err)
			} else {
				if format == "yaml" {
					document = "apiVersion: flightctl.io/v1beta1\nkind: EnrollmentApprovalPolicy\nmetadata:\n  name: west\nspec:\n  approvalLimit: 9223372036854775807\n"
				}
				require.Empty(t, applyFromReader(t.Context(), testClient, nil, "policy", strings.NewReader(document), false))
			}
			require.Equal(t, int64(9223372036854775807), received)
		})
	}
}

func TestApplyApprovalPolicyUsesResourceVersionAndOmitsStatus(t *testing.T) {
	for _, suppliedVersion := range []string{"", "2"} {
		t.Run(suppliedVersion, func(t *testing.T) {
			var methods []string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				methods = append(methods, request.Method)
				writer.Header().Set("Content-Type", "application/json")
				switch request.Method {
				case http.MethodPost:
					var body map[string]any
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					require.NotContains(t, body, "status")
					writer.WriteHeader(http.StatusConflict)
					_, _ = writer.Write([]byte(`{"code":409,"reason":"AlreadyExists","message":"already exists","status":"Failure"}`))
				case http.MethodGet:
					_, _ = writer.Write([]byte(`{"metadata":{"name":"west","resourceVersion":"7"}}`))
				case http.MethodPut:
					var body api.EnrollmentApprovalPolicy
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					require.Nil(t, body.Status)
					expected := suppliedVersion
					if expected == "" {
						expected = "7"
					}
					require.Equal(t, expected, *body.Metadata.ResourceVersion)
					writer.WriteHeader(http.StatusOK)
					_, _ = writer.Write([]byte(`{}`))
				}
			}))
			defer server.Close()
			generated, err := apiclient.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			body := []byte(`{"metadata":{"name":"west","resourceVersion":"` + suppliedVersion + `"},"status":{"enrollmentCount":5}}`)
			if suppliedVersion == "" {
				body = []byte(`{"metadata":{"name":"west"},"status":{"enrollmentCount":5}}`)
			}
			result := applyResourceByKind(t.Context(), client.NewTestClient(generated), nil, EnrollmentApprovalPolicyKind, "west", body)
			require.NoError(t, result.err)
			if suppliedVersion == "" {
				require.Equal(t, []string{"POST", "GET", "PUT"}, methods)
			} else {
				require.Equal(t, []string{"POST", "PUT"}, methods)
			}
		})
	}
}

func TestApplyApprovalPolicyPreservesReservedNameConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case http.MethodPost:
			writer.WriteHeader(http.StatusConflict)
			_, _ = writer.Write([]byte(`{"code":409,"reason":"AlreadyExists","message":"already exists","status":"Failure"}`))
		case http.MethodGet:
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"code":404,"reason":"NotFound","message":"policy not found"}`))
		default:
			t.Errorf("unexpected method %s", request.Method)
		}
	}))
	defer server.Close()
	generated, err := apiclient.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	result := applyResourceByKind(t.Context(), client.NewTestClient(generated), nil, EnrollmentApprovalPolicyKind, "west", []byte(`{"metadata":{"name":"west"}}`))
	require.NoError(t, result.err)
	require.NotNil(t, result.httpResponse)
	require.Equal(t, http.StatusConflict, result.httpResponse.StatusCode)
	require.NotNil(t, result.status)
	require.Equal(t, int32(409), result.status.Code)
	require.Equal(t, "AlreadyExists", result.status.Reason)
}

func TestApplySingleResourceEmptyName(t *testing.T) {
	tests := []struct {
		name        string
		resource    genericResource
		wantErr     bool
		errContains string
	}{
		{
			name: "empty name returns error",
			resource: genericResource{
				"apiVersion": "flightctl.io/v1alpha1",
				"kind":       "Catalog",
				"metadata": map[string]interface{}{
					"name": "",
				},
			},
			wantErr:     true,
			errContains: "metadata.name must not be empty",
		},
		{
			name: "missing name key returns error",
			resource: genericResource{
				"apiVersion": "flightctl.io/v1alpha1",
				"kind":       "Catalog",
				"metadata":   map[string]interface{}{},
			},
			wantErr:     true,
			errContains: "unspecified resource name",
		},
		{
			name: "missing metadata returns error",
			resource: genericResource{
				"apiVersion": "flightctl.io/v1alpha1",
				"kind":       "Catalog",
			},
			wantErr:     true,
			errContains: "unspecified metadata",
		},
		{
			name: "valid name proceeds (dry run)",
			resource: genericResource{
				"apiVersion": "flightctl.io/v1alpha1",
				"kind":       "Catalog",
				"metadata": map[string]interface{}{
					"name": "my-catalog",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			errs := applySingleResource(t.Context(), nil, nil, "test.yaml", tt.resource, true)
			if tt.wantErr {
				require.NotEmpty(errs)
				require.Contains(errs[0].Error(), tt.errContains)
			} else {
				require.Empty(errs)
			}
		})
	}
}

func TestApplyApprovalPolicyPreservesNonDuplicateConflict(t *testing.T) {
	for _, reason := range []string{"PolicyOverlap", "ImmutableCertificateBinding", "ResourceVersionConflict"} {
		t.Run(reason, func(t *testing.T) {
			var methods []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(api.NewFailureStatus(409, reason, "original conflict"))
			}))
			defer server.Close()
			generated, err := apiclient.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			result := applyResourceByKind(t.Context(), client.NewTestClient(generated), nil, EnrollmentApprovalPolicyKind, "west", []byte(`{"metadata":{"name":"west"}}`))
			require.NoError(t, result.err)
			require.Equal(t, []string{http.MethodPost}, methods)
			require.NotNil(t, result.status)
			require.Equal(t, reason, result.status.Reason)
		})
	}
}
