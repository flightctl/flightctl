package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentclient "github.com/flightctl/flightctl/internal/api/client/agent"
	"github.com/stretchr/testify/require"
)

func TestCreateEnrollmentRequestErrorReporting(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		wantMessage string
	}{
		{
			name:        "When the API rejects the enrollment credential it should include the status and server message",
			statusCode:  http.StatusUnauthorized,
			body:        `{"message":"Enrollment certificate is not approved"}`,
			wantMessage: "create enrollmentrequest failed: 401 Unauthorized: Enrollment certificate is not approved",
		},
		{
			name:        "When the API returns an empty body it should still include the status",
			statusCode:  http.StatusBadGateway,
			body:        "",
			wantMessage: "create enrollmentrequest failed: 502 Bad Gateway: empty response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, err := w.Write([]byte(tt.body))
				require.NoError(t, err)
			}))
			defer server.Close()

			clientWithResponses, err := agentclient.NewClientWithResponses(server.URL)
			require.NoError(t, err)

			name := "device-1"
			_, err = NewEnrollment(clientWithResponses, nil).CreateEnrollmentRequest(context.Background(), v1beta1.EnrollmentRequest{
				Metadata: v1beta1.ObjectMeta{Name: &name},
			})
			require.ErrorContains(t, err, tt.wantMessage)
		})
	}
}
