package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentclient "github.com/flightctl/flightctl/internal/api/client/agent"
	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestManagementFirmwarePolicy(t *testing.T) {
	view, err := firmwarepolicy.New(1, nil, nil)
	require.NoError(t, err)
	valid, err := json.Marshal(view)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		code    int
		payload string
		invalid bool
	}{
		{"When a complete view arrives it should validate it", 200, string(valid), false},
		{"When policy is unchanged it should return no view", 204, "", false},
		{"When the server is unavailable it should preserve the status", 503, `{"message":"unavailable"}`, false},
		{"When a response is malformed it should reject", 200, `{}`, true},
		{"When envelope fields repeat it should reject", 200, `{"revision":1,"revision":1}`, true},
		{"When a response exceeds the limit it should reject", 200, strings.Repeat(" ", firmwarepolicy.MaxWireBytes+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/devices/device/firmwarepolicy", r.URL.Path)
				require.Equal(t, "1", r.URL.Query().Get("knownRevision"))
				require.Equal(t, view.Digest, r.URL.Query().Get("knownDigest"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.payload))
			}))
			defer server.Close()
			generated, err := agentclient.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			management := NewManagement(generated, nil)
			got, status, err := management.GetFirmwarePolicy(context.Background(), "device", firmwarepolicy.Receipt{Revision: 1, Digest: view.Digest})
			require.Equal(t, tc.code, status)
			if tc.invalid {
				require.Error(t, err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				if tc.code == 200 {
					require.Equal(t, view, *got)
				} else {
					require.Nil(t, got)
				}
			}
		})
	}
}

func TestManagementFirmwarePolicyTransportErrors(t *testing.T) {
	receipt := firmwarepolicy.Receipt{Revision: 1, Digest: firmwarepolicy.Hash([]byte("document"))}
	t.Run("When the policy GET transport fails it should identify the operation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.Close()
		generated, err := agentclient.NewClientWithResponses(server.URL)
		require.NoError(t, err)
		management := NewManagement(generated, nil)
		_, _, err = management.GetFirmwarePolicy(context.Background(), "device", firmwarepolicy.Receipt{})
		require.ErrorContains(t, err, "get firmware policy:")
	})
	t.Run("When the policy acknowledgement transport fails it should identify the operation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.Close()
		generated, err := agentclient.NewClientWithResponses(server.URL)
		require.NoError(t, err)
		management := NewManagement(generated, nil)
		_, err = management.AcknowledgeFirmwarePolicy(context.Background(), "device", receipt)
		require.ErrorContains(t, err, "acknowledge firmware policy:")
	})
}

func TestManagementFirmwarePolicyReceiptAndReload(t *testing.T) {
	receipt := firmwarepolicy.Receipt{Revision: 1, Digest: firmwarepolicy.Hash([]byte("document"))}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		var got firmwarepolicy.Receipt
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		require.Equal(t, receipt, got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	generated, err := agentclient.NewClientWithResponses(server.URL)
	require.NoError(t, err)
	management := NewManagement(generated, nil)
	status, err := management.AcknowledgeFirmwarePolicy(context.Background(), "device", receipt)
	require.NoError(t, err)
	require.Equal(t, 204, status)
	ctrl := gomock.NewController(t)
	first := NewMockManagement(ctrl)
	second := NewMockManagement(ctrl)
	calls := 0
	delegate, err := NewManagementDelegate(func() (Management, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return second, nil
	})
	require.NoError(t, err)
	first.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", receipt).Return(nil, 204, nil)
	_, _, err = delegate.GetFirmwarePolicy(context.Background(), "device", receipt)
	require.NoError(t, err)
	require.NoError(t, delegate.Reload())
	second.EXPECT().AcknowledgeFirmwarePolicy(gomock.Any(), "device", receipt).Return(204, nil)
	_, err = delegate.AcknowledgeFirmwarePolicy(context.Background(), "device", receipt)
	require.NoError(t, err)
}
