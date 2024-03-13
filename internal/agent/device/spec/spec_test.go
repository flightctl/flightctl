package spec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestManager(t *testing.T) {
	tests := []struct {
		name           string
		ensureRendered bool
		wantSkipSync   bool
		wantErr        error
	}{
		{
			name:           "happy path",
			ensureRendered: true,
		},
		{
			name:           "error getting rendered spec during runtime",
			ensureRendered: false,
			wantErr:        ErrMissingRenderedSpec,
		},
		{
			name:           "skip sync 204 from api",
			ensureRendered: true,
			wantSkipSync:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()
			err := os.MkdirAll(tmpDir+"/etc/flightctl", 0755)
			require.NoError(err)
			currentSpecFilePath := "/etc/flightctl/" + "current-spec.json"
			desiredSpecFilePath := "/etc/flightctl/" + "desired-spec.json"
			backoff := wait.Backoff{
				Steps:    1,
				Duration: 1,
				Factor:   1,
				Jitter:   0,
			}
			server := createMockManagementServer(t, tt.wantSkipSync)
			defer server.Close()

			serverUrl := server.URL
			httpClient, err := testutil.NewClient(serverUrl, nil, nil)
			require.NoError(err)
			managementClient := client.NewManagement(httpClient)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			log := log.InitLogs()
			writer := fileio.NewWriter()
			writer.SetRootdir(tmpDir)
			reader := fileio.NewReader()
			reader.SetRootdir(tmpDir)

			// ensure rendered spec
			if tt.ensureRendered {
				_, err := EnsureCurrentRenderedSpec(ctx, log, "", writer, reader, "test", currentSpecFilePath)
				require.NoError(err)
			}

			manager := NewManager(
				"testDeviceName",
				currentSpecFilePath,
				desiredSpecFilePath,
				writer,
				reader,
				managementClient,
				backoff,
				log,
				"",
			)
			current, desired, skipSync, err := manager.GetRendered(ctx)
			if tt.wantSkipSync {
				require.Equal(tt.wantSkipSync, skipSync)
				return
			}
			if tt.wantErr != nil {
				require.ErrorAs(err, &tt.wantErr)
				return
			}
			require.NoError(err)
			// eval current
			require.Equal("", current.Owner)
			require.Equal("", current.TemplateVersion)
			// eval desired
			require.Equal("mockOwner", desired.Owner)
			require.Equal("mockTemplateVersion", desired.TemplateVersion)
		})
	}

}

func createMockManagementServer(t *testing.T, noChange bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mockTemplateVersion := "mockTemplateVersion"
		mockOwner := "mockOwner"
		resp := v1alpha1.RenderedDeviceSpec{
			Owner:           mockOwner,
			TemplateVersion: mockTemplateVersion,
		}

		w.Header().Set("Content-Type", "application/json")
		if noChange {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
		respBytes, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		_, err = w.Write(respBytes)
		if err != nil {
			t.Fatal(err)
		}
	}))
}
