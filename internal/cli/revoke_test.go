package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/require"
)

func TestRevokeValidate(t *testing.T) {
	configFile := writeMinimalClientConfig(t)

	tests := []struct {
		name        string
		args        []string
		reason      string
		wantErr     bool
		errContains string
	}{
		{
			name:   "When the CSR shorthand is supplied it should be accepted",
			args:   []string{"csr/credential-a"},
			reason: "credential compromised",
		},
		{
			name:   "When the singular kind and name are supplied it should be accepted",
			args:   []string{"certificatesigningrequest", "credential-a"},
			reason: "credential compromised",
		},
		{
			name:   "When the plural kind is supplied it should be accepted",
			args:   []string{"certificatesigningrequests/credential-a"},
			reason: "credential compromised",
		},
		{
			name:        "When a resource kind other than a CSR is supplied it should be rejected",
			args:        []string{"device/device-a"},
			reason:      "credential compromised",
			wantErr:     true,
			errContains: "kind must be certificatesigningrequest",
		},
		{
			name:        "When no CSR name is supplied it should be rejected",
			args:        []string{"csr"},
			reason:      "credential compromised",
			wantErr:     true,
			errContains: "exactly one resource name must be specified",
		},
		{
			name:        "When no reason is supplied it should be rejected",
			args:        []string{"csr/credential-a"},
			wantErr:     true,
			errContains: "--reason is required",
		},
		{
			name:        "When the reason is blank it should be rejected",
			args:        []string{"csr/credential-a"},
			reason:      "   ",
			wantErr:     true,
			errContains: "--reason is required",
		},
		{
			name:   "When the reason is exactly 1024 characters it should be accepted",
			args:   []string{"csr/credential-a"},
			reason: strings.Repeat("x", 1024),
		},
		{
			name:        "When the reason exceeds 1024 characters it should be rejected",
			args:        []string{"csr/credential-a"},
			reason:      strings.Repeat("x", 1025),
			wantErr:     true,
			errContains: "at most 1024 characters",
		},
		{
			name:   "When the reason contains 1024 multibyte characters it should be accepted",
			args:   []string{"csr/credential-a"},
			reason: strings.Repeat("é", 1024),
		},
		{
			name:        "When the reason exceeds 1024 multibyte characters it should be rejected",
			args:        []string{"csr/credential-a"},
			reason:      strings.Repeat("é", 1025),
			wantErr:     true,
			errContains: "at most 1024 characters",
		},
		{
			name:        "When extra resource names are supplied it should be rejected",
			args:        []string{"csr", "credential-a", "credential-b"},
			reason:      "credential compromised",
			wantErr:     true,
			errContains: "exactly one resource name must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultRevokeOptions()
			o.ConfigFilePath = configFile
			o.Reason = tt.reason

			err := o.Validate(tt.args)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRevokeDispatchesRevocationRequest(t *testing.T) {
	response := api.CertificateSigningRequest{
		ApiVersion: api.CertificateSigningRequestAPIVersion,
		Kind:       api.CertificateSigningRequestKind,
	}
	c, rec := newRecordingClient(t, http.StatusOK, response)

	o := DefaultRevokeOptions()
	o.Reason = "credential compromised"

	output := captureStdout(t, func() {
		require.NoError(t, o.revoke(context.Background(), c.ClientWithResponses, "credential-a"))
	})

	require.Contains(t, output, "credential-a")

	require.Len(t, rec.requests, 1)
	require.Equal(t, http.MethodPost, rec.requests[0].Method)
	require.Equal(t, "/certificatesigningrequests/credential-a/revocation", rec.requests[0].URL.Path)

	var body api.EnrollmentCertificateRevocationRequest
	require.NoError(t, json.Unmarshal([]byte(rec.bodies[0]), &body))
	require.Equal(t, "credential compromised", body.Reason)
}

func TestRevokeReportsAPIError(t *testing.T) {
	c, _ := newRecordingClient(t, http.StatusConflict, api.Status{
		Code:    http.StatusConflict,
		Message: "only issued enrollment credentials may be revoked",
	})

	o := DefaultRevokeOptions()
	o.Reason = "credential compromised"

	err := o.revoke(context.Background(), c.ClientWithResponses, "credential-a")
	require.Error(t, err)
	require.Contains(t, err.Error(), "revoking certificate signing request credential-a: failed")
	require.Contains(t, err.Error(), "only issued enrollment credentials may be revoked")
}

func TestCmdRevokeRequiresReason(t *testing.T) {
	cmd := NewCmdRevoke()
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"csr/credential-a"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), `required flag(s) "reason" not set`)
}
