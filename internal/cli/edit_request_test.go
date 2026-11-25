package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestEditPolicyRequestsOnlyForChanges(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		for _, changed := range []bool{false, true} {
			t.Run("When editing "+format+" with changed="+lo.Ternary(changed, "true", "false")+" it should patch only changes", func(t *testing.T) {
				policy := &api.EnrollmentApprovalPolicy{Kind: api.EnrollmentApprovalPolicyKind, ApiVersion: "flightctl.io/v1beta1", Metadata: api.ObjectMeta{Name: lo.ToPtr("west"), ResourceVersion: lo.ToPtr("1")}, Spec: api.EnrollmentApprovalPolicySpec{MatchExpression: "original"}}
				response := &apiclient.GetEnrollmentApprovalPolicyResponse{JSON200: policy, HTTPResponse: &http.Response{StatusCode: http.StatusOK}}
				options := DefaultEditOptions()
				options.Output = format
				content, err := options.resourceToFormat(response, format)
				require.NoError(t, err)
				if changed {
					content = []byte(strings.ReplaceAll(string(content), "original", "changed"))
				}
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requests++
					require.Equal(t, http.MethodPatch, request.Method)
					var patch api.PatchRequest
					require.NoError(t, json.NewDecoder(request.Body).Decode(&patch))
					require.Len(t, patch, 2)
					require.Equal(t, "test", string(patch[0].Op))
					require.Equal(t, "/metadata/resourceVersion", patch[0].Path)
					require.Equal(t, "1", patch[0].Value)
					require.Equal(t, "/spec/matchExpression", patch[1].Path)
					writer.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(writer, `{}`)
				}))
				defer server.Close()
				generated, err := apiclient.NewClientWithResponses(server.URL)
				require.NoError(t, err)
				require.NoError(t, options.applyChanges(t.Context(), generated, content, EnrollmentApprovalPolicyKind, "west", response))
				require.Equal(t, lo.Ternary(changed, 1, 0), requests)
			})
		}
	}
}
