package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/require"
)

func TestCmdOverrideDeviceFlag(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		statusCode int
		response   string
		wantError  string
	}{
		{
			name:       "When a flagged Device is supplied it should dismiss the flag",
			args:       []string{"device/device-a", "--reason", " Reviewed enrollment "},
			statusCode: http.StatusOK,
			response:   `{"kind":"Device","metadata":{"name":"device-a"}}`,
		},
		{
			name:       "When the Device has no revoked flag it should report the API conflict",
			args:       []string{"device", "device-a", "-r", "Reviewed enrollment"},
			statusCode: http.StatusConflict,
			response:   `{"code":409,"message":"device has no revoked enrollment credential flag to dismiss"}`,
			wantError:  "device has no revoked enrollment credential flag to dismiss",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type requestRecord struct {
				method string
				path   string
				body   api.DeviceEnrollmentCredentialOverrideRequest
				err    error
			}
			requests := make(chan requestRecord, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				record := requestRecord{method: r.Method, path: r.URL.Path}
				record.err = json.NewDecoder(r.Body).Decode(&record.body)
				requests <- record
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = io.WriteString(w, tt.response)
			}))
			defer server.Close()
			configDir := t.TempDir()
			config := fmt.Appendf(nil, "service:\n  server: %s\n", server.URL)
			require.NoError(t, os.WriteFile(filepath.Join(configDir, "client.yaml"), config, 0600))

			cmd := NewCmdOverride()
			cmd.SilenceErrors = true
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs(append([]string{"--config-dir", configDir}, tt.args...))
			err := cmd.ExecuteContext(t.Context())
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				var apiErr *APIError
				require.ErrorAs(t, err, &apiErr)
				require.Equal(t, int32(tt.statusCode), apiErr.Status.Code)
				require.Empty(t, output.String())
			} else {
				require.NoError(t, err)
				require.Contains(t, output.String(), `Dismissed revoked enrollment credential flag on device "device-a"`)
			}

			require.Len(t, requests, 1)
			record := <-requests
			require.NoError(t, record.err)
			require.Equal(t, http.MethodPost, record.method)
			require.Equal(t, "/api/v1/devices/device-a/enrollmentcredential/override", record.path)
			require.Equal(t, "Reviewed enrollment", record.body.Reason)
		})
	}
}

func TestCmdOverrideRejectsInvalidArguments(t *testing.T) {
	configDir := filepath.Dir(writeMinimalClientConfig(t))
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "When the reason flag is missing it should require the flag",
			args: []string{"device/device-a"},
			want: `required flag(s) "reason" not set`,
		},
		{
			name: "When the reason is blank it should reject the reason",
			args: []string{"device/device-a", "--reason", " "},
			want: "reason is required and cannot be blank",
		},
		{
			name: "When the reason exceeds the character limit it should reject the reason",
			args: []string{"device/device-a", "--reason", strings.Repeat("é", 1025)},
			want: "at most 1024 characters",
		},
		{
			name: "When a CSR is supplied it should require a Device",
			args: []string{"csr/credential-a", "--reason", "Reviewed enrollment"},
			want: "kind must be device",
		},
		{
			name: "When a Device name is missing it should require exactly one name",
			args: []string{"device", "--reason", "Reviewed enrollment"},
			want: "exactly one resource name must be specified",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmdOverride()
			cmd.SilenceErrors = true
			cmd.SetArgs(append([]string{"--config-dir", configDir}, tt.args...))
			require.ErrorContains(t, cmd.ExecuteContext(t.Context()), tt.want)
		})
	}
}
