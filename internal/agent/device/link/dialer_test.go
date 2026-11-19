package link

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestLinkURL(t *testing.T) {
	testCases := []struct {
		name    string
		server  string
		want    string
		wantErr bool
	}{
		{
			name:   "When the endpoint is https it should become a wss link URL",
			server: "https://agent.example.com:7443",
			want:   "wss://agent.example.com:7443/ws/v1/agent/link",
		},
		{
			name:   "When the endpoint has a trailing path it should be replaced by the link route",
			server: "https://agent.example.com:7443/some/base/",
			want:   "wss://agent.example.com:7443/ws/v1/agent/link",
		},
		{
			name:   "When the endpoint is http it should still dial wss because there is no plaintext form",
			server: "http://agent.example.com:7443",
			want:   "wss://agent.example.com:7443/ws/v1/agent/link",
		},
		{
			name:   "When the endpoint has surrounding whitespace it should be trimmed",
			server: "  https://agent.example.com:7443  ",
			want:   "wss://agent.example.com:7443/ws/v1/agent/link",
		},
		{
			name:    "When the endpoint is empty it should be rejected",
			server:  "",
			wantErr: true,
		},
		{
			name:    "When the endpoint has no host it should be rejected",
			server:  "/just/a/path",
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			cfg := &baseclient.Config{}
			cfg.Service.Server = tc.server

			got, err := linkURL(cfg)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
			require.Equal(tc.want, got)
		})
	}
}

// TestLinkTLSConfigReplacesALPNOnly is the guard for the dial's one real
// trap: the library reports an explicit failure when the handshake
// negotiates a protocol other than http/1.1, so a config that advertises h2
// breaks the dial outright.
func TestLinkTLSConfigReplacesALPNOnly(t *testing.T) {
	require := require.New(t)

	pool := x509.NewCertPool()
	base := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: "agent.example.com",
		RootCAs:    pool,
		// As a gRPC transport credential config would carry.
		NextProtos: []string{"h2"},
	}

	got := linkTLSConfig(base)

	require.Equal([]string{"http/1.1"}, got.NextProtos, "the link must not advertise h2")
	require.Equal(uint16(tls.VersionTLS13), got.MinVersion)
	require.Equal("agent.example.com", got.ServerName)
	require.Same(pool, got.RootCAs, "CA material is shared, only the ALPN list is replaced")
	require.False(got.InsecureSkipVerify, "the link adds no new way to disable verification")

	// The caller's config is untouched.
	require.Equal([]string{"h2"}, base.NextProtos)
}

// TestNewDialerLeavesProxyNil pins the behaviour that makes this a direct
// dial: DefaultDialer would consult the process environment for a proxy.
func TestNewDialerLeavesProxyNil(t *testing.T) {
	require := require.New(t)

	d := newDialer(&tls.Config{MinVersion: tls.VersionTLS13})

	require.Nil(d.Proxy, "the link dial must never be redirected by the process environment")
	require.Nil(d.NetDialTLSContext, "setting it would make the library ignore TLSClientConfig")
	require.Equal([]string{protocol.Subprotocol}, d.Subprotocols)
	require.Equal(protocol.HandshakeTimeout, d.HandshakeTimeout)
	require.NotNil(d.TLSClientConfig)

	// websocket.DefaultDialer is what this deliberately is not.
	require.NotNil(websocket.DefaultDialer.Proxy)
}

func TestBackoffIsFullJitterAndBounded(t *testing.T) {
	require := require.New(t)

	// The first retry is drawn uniformly from 0-30s.
	sawBelowHalf := false
	sawAboveHalf := false
	for i := 0; i < 200; i++ {
		d := backoff(0)
		require.GreaterOrEqual(d, time.Duration(0))
		require.Less(d, protocol.ReconnectBackoffBase)
		if d < protocol.ReconnectBackoffBase/2 {
			sawBelowHalf = true
		} else {
			sawAboveHalf = true
		}
	}
	require.True(sawBelowHalf && sawAboveHalf, "the delay should be jittered across the window, not fixed")

	// Later attempts widen the window but never past the cap.
	for attempt := 0; attempt < 40; attempt++ {
		for i := 0; i < 20; i++ {
			d := backoff(attempt)
			require.GreaterOrEqual(d, time.Duration(0))
			require.LessOrEqual(d, protocol.ReconnectBackoffCap)
		}
	}
}

func TestDescribeDialFailureKeepsFailureDomainsDistinct(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		resp     *http.Response
		contains string
	}{
		{
			name:     "When no HTTP response exists it should report the transport error unchanged",
			err:      errTLSFailure,
			resp:     nil,
			contains: "tls handshake failed",
		},
		{
			name:     "When the service answers 404 it should report the route as unavailable",
			err:      websocket.ErrBadHandshake,
			resp:     newResponse(http.StatusNotFound),
			contains: "not available",
		},
		{
			name:     "When the service answers 426 it should report the HTTP version requirement",
			err:      websocket.ErrBadHandshake,
			resp:     newResponse(http.StatusUpgradeRequired),
			contains: "HTTP/1.1",
		},
		{
			name:     "When the service answers 403 it should report a refusal",
			err:      websocket.ErrBadHandshake,
			resp:     newResponse(http.StatusForbidden),
			contains: "refused",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			err := describeDialFailure(tc.err, tc.resp)
			require.Error(err)
			require.Contains(err.Error(), tc.contains)
		})
	}
}

var errTLSFailure = &tlsError{}

type tlsError struct{}

func (e *tlsError) Error() string { return "tls handshake failed" }

func newResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       http.NoBody,
	}
}
