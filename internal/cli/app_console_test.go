package cli

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppConsoleOptions_Validate(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		appName     string
		consoleType string
		tty         bool
		noTTY       bool
		exposedPort int
		wantErr     bool
		errContains string
	}{
		{
			name:        "When --name and --type serial are provided it should succeed",
			args:        []string{"device/mydevice"},
			appName:     "myvm",
			consoleType: "serial",
			wantErr:     false,
		},
		{
			name:        "When --name and --type vnc are provided it should succeed",
			args:        []string{"device/mydevice"},
			appName:     "myvm",
			consoleType: "vnc",
			wantErr:     false,
		},
		{
			name:        "When --type is not serial or vnc it should return an error",
			args:        []string{"device/mydevice"},
			appName:     "myvm",
			consoleType: "bogus",
			wantErr:     true,
			errContains: `--type must be "serial" or "vnc"`,
		},
		{
			name:        "When non-device kind is provided it should return an error",
			args:        []string{"fleet/myfarm"},
			appName:     "myvm",
			consoleType: "serial",
			wantErr:     true,
			errContains: "only devices can be connected to an application console",
		},
		{
			name:        "When --tty and --notty are both set it should return an error",
			args:        []string{"device/mydevice"},
			appName:     "myvm",
			consoleType: "serial",
			tty:         true,
			noTTY:       true,
			wantErr:     true,
			errContains: "--tty and --notty are mutually exclusive",
		},
		{
			name:        "When --exposed-port is set with --type serial it should return an error",
			args:        []string{"device/mydevice"},
			appName:     "myvm",
			consoleType: "serial",
			exposedPort: 5900,
			wantErr:     true,
			errContains: "--exposed-port is only valid with --type vnc",
		},
		{
			name:        "When --exposed-port is out of range it should return an error",
			args:        []string{"device/mydevice"},
			appName:     "myvm",
			consoleType: "vnc",
			exposedPort: 70000,
			wantErr:     true,
			errContains: "--exposed-port must be between 0 and 65535",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultAppConsoleOptions()
			o.name = tt.appName
			o.consoleType = tt.consoleType
			o.tty = tt.tty
			o.noTTY = tt.noTTY
			o.exposedPort = tt.exposedPort

			err := o.Validate(tt.args)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBuildAppConsoleURL(t *testing.T) {
	tests := []struct {
		name          string
		consoleServer string
		deviceName    string
		appName       string
		consoleType   string
		wantScheme    string
		wantPath      string
	}{
		{
			name:          "When server uses https it should produce a wss URL",
			consoleServer: "https://console.example.com",
			deviceName:    "dev1",
			appName:       "myvm",
			consoleType:   "serial",
			wantScheme:    "wss",
			wantPath:      "/ws/v1/devices/dev1/applications/myvm/console",
		},
		{
			name:          "When server uses http it should produce a ws URL",
			consoleServer: "http://console.example.com",
			deviceName:    "dev1",
			appName:       "myvm",
			consoleType:   "serial",
			wantScheme:    "ws",
			wantPath:      "/ws/v1/devices/dev1/applications/myvm/console",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultAppConsoleOptions()
			o.consoleType = tt.consoleType

			got, err := o.buildAppConsoleURL(tt.consoleServer, tt.deviceName, tt.appName)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(got, tt.wantScheme+"://"), "expected scheme %s in %s", tt.wantScheme, got)
			assert.Contains(t, got, tt.wantPath)
			assert.Contains(t, got, "consoleType="+tt.consoleType)
		})
	}
}

func TestConnectAppViaWS_HTTPErrors(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		errContains string
	}{
		{
			name:        "When server returns 403 it should report auth error",
			statusCode:  http.StatusForbidden,
			body:        "Viewer role is not permitted",
			errContains: "403",
		},
		{
			name:        "When server returns 409 it should report duplicate session error",
			statusCode:  http.StatusConflict,
			body:        "a serial console session is already active",
			errContains: "409",
		},
		{
			name:        "When server returns 504 it should report timeout error",
			statusCode:  http.StatusGatewayTimeout,
			body:        "timed out waiting for agent",
			errContains: "504",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, tt.body, tt.statusCode)
			}))
			defer srv.Close()

			cfg := &client.Config{
				RemoteAccessService: &client.Service{
					Server:             srv.URL,
					InsecureSkipVerify: true,
				},
			}

			o := DefaultAppConsoleOptions()
			o.consoleType = "serial"

			err := o.connectAppViaWS(t.Context(), cfg, "dev1", "myvm", "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// TestConnectAppViaWS_SessionError_ReturnsConsoleSessionError verifies that when the
// server closes an already-upgraded WebSocket with consts.AppConsoleErrorCloseCode
// (signalling a session-level failure reported by the agent, e.g. app not found),
// connectAppViaWS surfaces it as a *ConsoleSessionError rather than treating the
// close as a normal, successful end of session.
func TestConnectAppViaWS_SessionError_ReturnsConsoleSessionError(t *testing.T) {
	const agentErr = "app is not a VM workload"

	// clientDone is closed once connectAppViaWS returns, so the server handler below can
	// wait for the client to actually finish processing the close frame instead of relying
	// on a fixed sleep that would be flaky under load.
	clientDone := make(chan struct{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(consts.AppConsoleErrorCloseCode, agentErr),
			time.Now().Add(5*time.Second),
		)
		// Wait for the client to finish reading the close frame before the handler
		// returns and the test server tears down the connection.
		select {
		case <-clientDone:
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	cfg := &client.Config{
		RemoteAccessService: &client.Service{
			Server:             srv.URL,
			InsecureSkipVerify: true,
		},
	}

	// connectAppViaWS reads from the real os.Stdin when not attached to a terminal.
	// Swap in a pipe that blocks until closed, so the stdin-forwarding goroutine
	// cannot race ahead and return via `done` before the recv goroutine observes
	// the session error close frame (a real, non-piped stdin may EOF immediately
	// in CI, which would make that race non-deterministic).
	stdinR, stdinW, err := os.Pipe()
	require.NoError(t, err)
	defer stdinW.Close()
	defer stdinR.Close()
	oldStdin := os.Stdin
	os.Stdin = stdinR
	defer func() { os.Stdin = oldStdin }()

	o := DefaultAppConsoleOptions()
	o.consoleType = "serial"
	o.noTTY = true

	err = o.connectAppViaWS(t.Context(), cfg, "dev1", "myvm", "")
	close(clientDone)

	var sessionErr *ConsoleSessionError
	require.ErrorAs(t, err, &sessionErr)
	assert.Equal(t, agentErr, sessionErr.Message)
}

func TestBuildTLSConfigForConsole(t *testing.T) {
	caKeyPair := generateSelfSignedCert(t)

	tests := []struct {
		name        string
		consoleSvc  client.Service
		authInfo    client.AuthInfo
		wantRootCAs bool
		wantCerts   bool
		wantErrCA   bool
		wantErrCert bool
		serverName  string
		insecure    bool
	}{
		{
			name:       "When no TLS data is provided it should return a config with no extra CAs or certs",
			consoleSvc: client.Service{},
		},
		{
			name:        "When valid CA data is provided it should populate RootCAs",
			consoleSvc:  client.Service{CertificateAuthorityData: caKeyPair.certPEM},
			wantRootCAs: true,
		},
		{
			name:       "When invalid CA data is provided it should return an error",
			consoleSvc: client.Service{CertificateAuthorityData: []byte("not-a-cert")},
			wantErrCA:  true,
		},
		{
			name: "When valid client cert is provided it should add the certificate",
			authInfo: client.AuthInfo{
				ClientCertificateData: caKeyPair.certPEM,
				ClientKeyData:         caKeyPair.keyPEM,
			},
			wantCerts: true,
		},
		{
			name: "When invalid client cert data is provided it should return an error",
			authInfo: client.AuthInfo{
				ClientCertificateData: []byte("bad-cert"),
				ClientKeyData:         []byte("bad-key"),
			},
			wantErrCert: true,
		},
		{
			name:       "When TLSServerName is set it should be present in the TLS config",
			consoleSvc: client.Service{TLSServerName: "myserver.example.com"},
			serverName: "myserver.example.com",
		},
		{
			name:       "When InsecureSkipVerify is true it should be set in the TLS config",
			consoleSvc: client.Service{InsecureSkipVerify: true},
			insecure:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildTLSConfigForConsole(&tt.consoleSvc, tt.authInfo)
			if tt.wantErrCA || tt.wantErrCert {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)
			if tt.wantRootCAs {
				assert.NotNil(t, got.RootCAs)
			} else {
				assert.Nil(t, got.RootCAs)
			}
			if tt.wantCerts {
				assert.NotEmpty(t, got.Certificates)
			} else {
				assert.Empty(t, got.Certificates)
			}
			assert.Equal(t, tt.serverName, got.ServerName)
			assert.Equal(t, tt.insecure, got.InsecureSkipVerify)
		})
	}
}

// certKeyPair holds PEM-encoded self-signed cert and key for testing.
type certKeyPair struct {
	certPEM []byte
	keyPEM  []byte
}

// extractServerCert extracts the PEM-encoded server certificate from a TLS test server.
func extractServerCert(t *testing.T, srv *httptest.Server) []byte {
	t.Helper()
	certDER := srv.TLS.Certificates[0].Certificate[0]
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
}

// generateSelfSignedCert creates a minimal self-signed cert/key pair for test use by
// extracting them from a short-lived httptest.TLSServer.
func generateSelfSignedCert(t *testing.T) certKeyPair {
	t.Helper()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	certDER := srv.TLS.Certificates[0].Certificate[0]
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	privKey := srv.TLS.Certificates[0].PrivateKey
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

	return certKeyPair{certPEM: certPEM, keyPEM: keyPEM}
}

func TestConnectVNCViaWS_MissingRemoteAccessService(t *testing.T) {
	o := DefaultAppConsoleOptions()
	o.consoleType = "vnc"

	cfg := &client.Config{}
	err := o.connectVNCViaWS(context.Background(), cfg, "dev1", "myvm", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote access service is not configured")
}

func TestConnectVNCViaWS_HTTPError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	o := DefaultAppConsoleOptions()
	o.consoleType = "vnc"

	cfg := &client.Config{
		RemoteAccessService: &client.Service{
			Server:                   srv.URL,
			CertificateAuthorityData: extractServerCert(t, srv),
		},
	}
	err := o.connectVNCViaWS(context.Background(), cfg, "dev1", "myvm", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// TestConnectVNCViaWS_SessionError_ReturnsConsoleSessionError verifies that connectVNCViaWS
// mirrors connectAppViaWS's handling of consts.AppConsoleErrorCloseCode: a session-level
// failure reported by the agent (e.g. app not found) must surface as a *ConsoleSessionError,
// not a generic "VNC tunnel connection lost" error.
func TestConnectVNCViaWS_SessionError_ReturnsConsoleSessionError(t *testing.T) {
	const agentErr = "app is not a VM workload"

	// clientDone is closed once connectVNCViaWS returns, so the server handler below can
	// wait for the client to actually finish processing the close frame instead of relying
	// on a fixed sleep that would be flaky under load.
	clientDone := make(chan struct{})
	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(consts.AppConsoleErrorCloseCode, agentErr),
			time.Now().Add(5*time.Second),
		)
		select {
		case <-clientDone:
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	o := DefaultAppConsoleOptions()
	o.consoleType = "vnc"

	cfg := &client.Config{
		RemoteAccessService: &client.Service{
			Server:             srv.URL,
			InsecureSkipVerify: true,
		},
	}

	err := o.connectVNCViaWS(t.Context(), cfg, "dev1", "myvm", "")
	close(clientDone)

	var sessionErr *ConsoleSessionError
	require.ErrorAs(t, err, &sessionErr)
	assert.Equal(t, agentErr, sessionErr.Message)
}

func TestBridgeVNCClient_ClientDisconnectDoesNotCloseWebSocket(t *testing.T) {
	// Set up a WebSocket echo server that stays open for the duration of the test.
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	wsServerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(wsServerDone)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer wsConn.Close()

	wsRecvCh := make(chan []byte, 64)

	// Run the WebSocket reader goroutine.
	go func() {
		defer close(wsRecvCh)
		for {
			_, data, err := wsConn.ReadMessage()
			if err != nil {
				return
			}
			wsRecvCh <- data
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First VNC client: connect, send data, disconnect.
	serverSide, clientSide := net.Pipe()
	go func() {
		defer serverSide.Close()
		_, _ = serverSide.Write([]byte("hello"))
		// read the echo back
		buf := make([]byte, 5)
		_, _ = serverSide.Read(buf)
		// disconnect
	}()

	o := DefaultAppConsoleOptions()
	o.bridgeVNCClient(ctx, clientSide, wsConn, wsRecvCh)
	clientSide.Close()

	// WebSocket should still be open: send a second message.
	require.NoError(t, wsConn.WriteMessage(websocket.BinaryMessage, []byte("still-open")))

	// Confirm the echo server is still running (wsServerDone not closed yet).
	select {
	case <-wsServerDone:
		t.Fatal("WebSocket server closed unexpectedly after first VNC client disconnected")
	case <-time.After(100 * time.Millisecond):
		// expected: server still running
	}
}

func TestConnectAppViaWS_MissingRemoteAccessService(t *testing.T) {
	o := DefaultAppConsoleOptions()
	o.consoleType = "serial"

	cfg := &client.Config{}

	err := o.connectAppViaWS(t.Context(), cfg, "dev1", "myvm", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote access service is not configured")
}
