package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/util/httpstream"
	certutil "k8s.io/client-go/util/cert"
	k8sTerm "k8s.io/kubectl/pkg/util/term"
)

// maxAppConsoleWSMessageSize caps the size of a single websocket message (and, for the
// handshake failure path, the error body read) to prevent a malicious or compromised
// server from forcing the CLI to allocate unbounded memory.
const maxAppConsoleWSMessageSize = 1 << 20 // 1 MiB, matches internal/remote_access_server/ws_handler.go

// vncWSWriteTimeout bounds each write to the persistent VNC WebSocket tunnel so a stalled
// remote peer cannot block the bridging goroutines (and therefore ctx cancellation) forever.
const vncWSWriteTimeout = 10 * time.Second

// vncClientAttachedPollInterval bounds how long deliverVNCData blocks on a full wsRecvCh
// before re-checking whether the attached client is still attached.
const vncClientAttachedPollInterval = 50 * time.Millisecond

// deliverVNCData sends data to wsRecvCh. If a client is attached, it blocks (applying
// backpressure) but re-checks clientAttached periodically rather than committing to a
// single indefinite blocking send — otherwise, if the client disconnects while this call
// is blocked on a full buffer, nothing will ever drain wsRecvCh again (the next viewer's
// consumer goroutine hasn't started yet), and this send would block forever. If no client
// is attached and the buffer is full, the data is dropped instead of stalling the caller.
// Returns a non-nil error only when ctx is done, signaling the caller should stop reading.
func deliverVNCData(ctx context.Context, wsRecvCh chan<- []byte, clientAttached *atomic.Bool, data []byte) error {
	for clientAttached.Load() {
		select {
		case wsRecvCh <- data:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(vncClientAttachedPollInterval):
			// The attached client may have disconnected while we were blocked on a
			// full buffer; loop back around to re-check clientAttached.
		}
	}
	select {
	case wsRecvCh <- data:
	case <-ctx.Done():
		return ctx.Err()
	default:
		// No client attached and buffer full: discard to avoid stalling the
		// WebSocket reader while idle.
	}
	return nil
}

// ConsoleSessionError indicates the server or agent reported a session-level failure
// (e.g. the requested application does not exist) over an already-established
// WebSocket connection, as opposed to a transport/handshake-level error.
type ConsoleSessionError struct {
	Message string
}

func (e *ConsoleSessionError) Error() string {
	return e.Message
}

// AppConsoleOptions holds the options for the "app console" command, which connects to the
// serial or VNC console of a VM application running on a device.
type AppConsoleOptions struct {
	GlobalOptions
	tty         bool
	noTTY       bool
	remoteTTY   bool
	name        string
	consoleType string
	exposedPort int
	force       bool
}

func DefaultAppConsoleOptions() *AppConsoleOptions {
	return &AppConsoleOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewCmdAppConsole() *cobra.Command {
	o := DefaultAppConsoleOptions()

	cmd := &cobra.Command{
		Use:   "console device/NAME --name APP --type serial|vnc",
		Short: "Connect a console to a VM application running on a device through the server.",
		Example: `  # Connect to an application's serial console
  flightctl app console device/my-device --name my-app --type serial

  # Connect to an application's VNC console
  flightctl app console device/my-device --name my-app --type vnc`,
		Args: cobra.MinimumNArgs(1),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{DeviceKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), args)
		},
		SilenceUsage: true,
	}

	o.Bind(cmd.Flags())
	markAppConsoleFlagsRequired(cmd)

	return cmd
}

// markAppConsoleFlagsRequired declares --name and --type required on cmd so cobra rejects a
// missing value immediately and documents the requirement in --help.
func markAppConsoleFlagsRequired(cmd *cobra.Command) {
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("type")
}

func (o *AppConsoleOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.BoolVarP(&o.tty, "tty", "", o.tty, "Allocate remote pseudo terminal")
	fs.BoolVarP(&o.noTTY, "notty", "", o.noTTY, "Don't allocate remote pseudo terminal")
	fs.StringVar(&o.name, "name", o.name, "Application name to open a console for (required)")
	fs.StringVar(&o.consoleType, "type", o.consoleType, "Console type: serial or vnc (required)")
	fs.IntVar(&o.exposedPort, "exposed-port", o.exposedPort, "Local TCP port for VNC port-forward (0 = random ephemeral port; only valid with --type vnc)")
	fs.BoolVar(&o.force, "force", o.force, "Take over an already-active console session for the same --name, disconnecting it")
}

func (o *AppConsoleOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *AppConsoleOptions) Validate(args []string) error {
	if len(args) > 2 {
		return fmt.Errorf("arguments must be of the form 'device/NAME' or 'device NAME'")
	}
	kind, name, err := parseAndValidateKindNameFromArgsSingle(args)
	if err != nil {
		return err
	}

	if kind != DeviceKind {
		return fmt.Errorf("only devices can be connected to an application console")
	}

	if len(name) == 0 {
		return fmt.Errorf("device name is required")
	}

	if o.tty && o.noTTY {
		return fmt.Errorf("--tty and --notty are mutually exclusive")
	}

	if o.consoleType != "serial" && o.consoleType != "vnc" {
		return fmt.Errorf("--type must be \"serial\" or \"vnc\", got %q", o.consoleType)
	}

	if o.exposedPort < 0 || o.exposedPort > 65535 {
		return fmt.Errorf("--exposed-port must be between 0 and 65535, got %d", o.exposedPort)
	}

	if o.exposedPort != 0 && o.consoleType != "vnc" {
		return fmt.Errorf("--exposed-port is only valid with --type vnc")
	}

	return nil
}

func (o *AppConsoleOptions) Run(ctx context.Context, args []string) error {
	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	_, name, err := parseAndValidateKindNameFromArgsSingle(args)
	if err != nil {
		return err
	}

	refresher := client.NewAccessTokenRefresher(config, o.ConfigFilePath, 8080)
	refresher.Start(ctx)
	accessToken := refresher.GetAccessToken()

	if o.consoleType == "vnc" {
		analyzeResponseAndExit(ctx, &o.GlobalOptions, name, o.connectVNCViaWS(ctx, config, name, o.name, accessToken))
		return nil // unreachable
	}
	analyzeResponseAndExit(ctx, &o.GlobalOptions, name, o.connectAppViaWS(ctx, config, name, o.name, accessToken))
	return nil // unreachable
}

func (o *AppConsoleOptions) SetupTTY() k8sTerm.TTY {
	t := k8sTerm.TTY{
		In:  os.Stdin,
		Out: os.Stdout,
	}

	o.remoteTTY = o.tty || t.IsTerminalIn() && t.IsTerminalOut() && !o.noTTY
	t.Raw = o.remoteTTY && t.IsTerminalIn()

	return t
}

// buildAppConsoleURL constructs the WebSocket URL for the VM application serial or VNC console.
// The base URL's scheme is converted from https/http to wss/ws as required by gorilla/websocket.
func (o *AppConsoleOptions) buildAppConsoleURL(consoleServer, deviceName, appName string) (string, error) {
	u, err := url.Parse(fmt.Sprintf("%s/ws/v1/devices/%s/applications/%s/console", consoleServer, url.PathEscape(deviceName), url.PathEscape(appName)))
	if err != nil {
		return "", fmt.Errorf("parsing console URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	q := url.Values{}
	q.Set("consoleType", o.consoleType)
	q.Set(api.OrganizationIDQueryKey, o.GetEffectiveOrganization())
	if o.force {
		q.Set("force", "true")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// buildTLSConfigForConsole creates a tls.Config from the RemoteAccessService TLS settings.
func buildTLSConfigForConsole(consoleSvc *client.Service, authInfo client.AuthInfo) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: consoleSvc.InsecureSkipVerify, //nolint:gosec
	}
	if consoleSvc.TLSServerName != "" {
		tlsCfg.ServerName = consoleSvc.TLSServerName
	}
	if len(consoleSvc.CertificateAuthorityData) > 0 {
		caPool, err := certutil.NewPoolFromBytes(consoleSvc.CertificateAuthorityData)
		if err != nil {
			return nil, fmt.Errorf("parsing console service CA: %w", err)
		}
		tlsCfg.RootCAs = caPool
	}
	if len(authInfo.ClientCertificateData) > 0 {
		clientCert, err := tls.X509KeyPair(authInfo.ClientCertificateData, authInfo.ClientKeyData)
		if err != nil {
			return nil, fmt.Errorf("parsing client certificate: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{clientCert}
	}
	return tlsCfg, nil
}

// connectAppViaWS opens a binary WebSocket connection to flightctl-remote-access and
// bridges stdin/stdout, applying the same ~. escape sequence as the device console.
func (o *AppConsoleOptions) connectAppViaWS(ctx context.Context, config *client.Config, deviceName, appName, token string) error {
	consoleServer := config.GetRemoteAccessServer()
	if consoleServer == "" {
		return fmt.Errorf("remote access service is not configured; run 'flightctl login' to update your client config or set 'remoteAccessService.server' manually")
	}

	connURL, err := o.buildAppConsoleURL(consoleServer, deviceName, appName)
	if err != nil {
		return err
	}

	tlsCfg, err := buildTLSConfigForConsole(config.RemoteAccessService, config.AuthInfo)
	if err != nil {
		return err
	}

	dialer := websocket.Dialer{
		TLSClientConfig: tlsCfg,
	}
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}

	conn, resp, err := dialer.DialContext(ctx, connURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxAppConsoleWSMessageSize))
			msg := strings.TrimSpace(string(body))
			return &httpstream.UpgradeFailureError{
				Cause: fmt.Errorf("websocket: bad handshake (%d %s): %s", resp.StatusCode, http.StatusText(resp.StatusCode), msg),
			}
		}
		return err
	}
	defer conn.Close()
	conn.SetReadLimit(maxAppConsoleWSMessageSize)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	t := o.SetupTTY()
	var oldState *term.State
	var stdinReader io.Reader
	if t.Raw {
		stdinReader = newRawReader(cancel, &oldState)
	} else if !t.IsTerminalIn() {
		stdinReader = os.Stdin
	}

	if t.Raw {
		defer func() {
			if oldState != nil {
				if err := term.Restore(int(os.Stdin.Fd()), oldState); err != nil {
					fmt.Printf("error restoring terminal: %v", err)
				}
			}
		}()
	}

	fmt.Fprintf(os.Stderr, "Connected to %s console. Use ~. to exit.\r\n", appName)

	done := make(chan struct{})

	// stdin → WebSocket
	go func() {
		defer func() {
			conn.WriteMessage(websocket.CloseMessage, //nolint:errcheck
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			close(done)
		}()
		if stdinReader == nil {
			// No stdin source; block until context is cancelled rather than
			// immediately sending a close frame and ending the session.
			<-ctx.Done()
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := stdinReader.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket → stdout
	// sessionErr is written by the recv goroutine below and read after the select; since the
	// select can also wake up via <-done (e.g. stdin closing at the same time) rather than
	// <-ctx.Done(), there is no guaranteed happens-before edge on a plain variable. Use
	// atomic.Value, matching the tunnelErr pattern in connectVNCViaWS.
	var sessionErr atomic.Value
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				// The server closes with consts.AppConsoleErrorCloseCode (instead of a
				// normal closure) when the session failed server- or agent-side (e.g. the
				// requested application does not exist) — surface that as a real error
				// rather than a silent, successful exit.
				var closeErr *websocket.CloseError
				if errors.As(err, &closeErr) && closeErr.Code == consts.AppConsoleErrorCloseCode {
					sessionErr.Store(error(&ConsoleSessionError{Message: closeErr.Text}))
					cancel()
					return
				}
				// A normal close from the remote end is not an error; avoid
				// cancelling the context so connectAppViaWS returns nil.
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					cancel()
				}
				return
			}
			if msgType == websocket.BinaryMessage || msgType == websocket.TextMessage {
				if _, werr := os.Stdout.Write(msg); werr != nil {
					cancel()
					return
				}
			}
		}
	}()

	select {
	case <-done:
	case <-recvDone:
	case <-ctx.Done():
	}

	if err, ok := sessionErr.Load().(error); ok {
		return err
	}
	return ctx.Err()
}

// connectVNCViaWS opens a WebSocket session to flightctl-remote-access and listens on a local
// TCP port for a single VNC viewer connection, then bridges it to the WebSocket. The agent-side
// VNC server completes its RFB handshake with that one viewer and cannot re-handshake on the
// same tunnel, so the command ends as soon as the viewer disconnects (run it again to start a
// new session); it can also end earlier if the context is canceled (Ctrl+C) or the tunnel fails.
// The local port is determined by --exposed-port (0 = random ephemeral).
func (o *AppConsoleOptions) connectVNCViaWS(ctx context.Context, config *client.Config, deviceName, appName, token string) error {
	consoleServer := config.GetRemoteAccessServer()
	if consoleServer == "" {
		return fmt.Errorf("remote access service is not configured; run 'flightctl login' to update your client config or set 'remoteAccessService.server' manually")
	}

	connURL, err := o.buildAppConsoleURL(consoleServer, deviceName, appName)
	if err != nil {
		return err
	}

	tlsCfg, err := buildTLSConfigForConsole(config.RemoteAccessService, config.AuthInfo)
	if err != nil {
		return err
	}

	dialer := websocket.Dialer{TLSClientConfig: tlsCfg}
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}

	wsConn, resp, err := dialer.DialContext(ctx, connURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxAppConsoleWSMessageSize))
			msg := strings.TrimSpace(string(body))
			return &httpstream.UpgradeFailureError{
				Cause: fmt.Errorf("websocket: bad handshake (%d %s): %s", resp.StatusCode, http.StatusText(resp.StatusCode), msg),
			}
		}
		return err
	}
	defer wsConn.Close()
	wsConn.SetReadLimit(maxAppConsoleWSMessageSize)

	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", o.exposedPort))
	if err != nil {
		return fmt.Errorf("creating local VNC listener: %w", err)
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	fmt.Fprintf(os.Stderr, "VNC is available at localhost:%d — connect your VNC viewer. The session ends when the viewer disconnects (or press Ctrl+C to exit early).\r\n", addr.Port)

	// wsRecvCh buffers VNC data arriving from the server between client connections.
	// A bounded buffer avoids unbounded memory growth while idle; data beyond the buffer
	// capacity is discarded only while no client is attached, since the next client will
	// re-sync via the RFB handshake. While a client IS attached, dropping would silently
	// corrupt its live byte stream, so the reader instead applies backpressure (blocks)
	// until the client's consumer goroutine drains the buffer.
	wsRecvCh := make(chan []byte, 64)
	var clientAttached atomic.Bool

	// tunnelCtx is canceled when the persistent WebSocket tunnel dies (read error), separately
	// from ctx being canceled by the user. This lets the accept loop below stop accepting new
	// local clients once the tunnel is gone, instead of accepting clients that would immediately
	// tear down against an already-closed wsRecvCh.
	tunnelCtx, tunnelCancel := context.WithCancel(ctx)
	defer tunnelCancel()
	var tunnelErr atomic.Value

	go func() {
		defer close(wsRecvCh)
		defer tunnelCancel()
		for {
			_, data, err := wsConn.ReadMessage()
			if err != nil {
				if ctx.Err() == nil {
					// The server closes with consts.AppConsoleErrorCloseCode (instead of a
					// normal closure) when the session failed server- or agent-side (e.g. the
					// requested application does not exist) — surface that as a clean,
					// recognizable error rather than a generic "tunnel connection lost".
					var closeErr *websocket.CloseError
					if errors.As(err, &closeErr) && closeErr.Code == consts.AppConsoleErrorCloseCode {
						tunnelErr.Store(error(&ConsoleSessionError{Message: closeErr.Text}))
					} else {
						tunnelErr.Store(err)
					}
				}
				return
			}
			if err := deliverVNCData(ctx, wsRecvCh, &clientAttached, data); err != nil {
				return
			}
		}
	}()

	tcpListener := listener.(*net.TCPListener)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if tunnelCtx.Err() != nil {
			return vncTunnelError(&tunnelErr)
		}

		// Use a short deadline so Accept() wakes up regularly to check ctx.
		if err := tcpListener.SetDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return fmt.Errorf("setting listener deadline: %w", err)
		}

		tcpConn, err := tcpListener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return fmt.Errorf("accepting VNC client connection: %w", err)
		}

		clientAttached.Store(true)
		o.bridgeVNCClient(tunnelCtx, tcpConn, wsConn, wsRecvCh)
		clientAttached.Store(false)
		tcpConn.Close()

		// The agent-side VNC server already completed its RFB handshake with this
		// viewer and cannot re-handshake on the same tunnel, so a second viewer could
		// never work here. End the command now instead of looping back to Accept().
		return vncTunnelError(&tunnelErr)
	}
}

// vncTunnelError returns the error to report for the persistent VNC tunnel having gone away, or
// nil if it hasn't (e.g. the caller is returning because the viewer disconnected normally rather
// than because the tunnel failed).
func vncTunnelError(tunnelErr *atomic.Value) error {
	err, ok := tunnelErr.Load().(error)
	if !ok {
		return nil
	}
	var sessionErr *ConsoleSessionError
	if errors.As(err, &sessionErr) {
		return sessionErr
	}
	return fmt.Errorf("VNC tunnel connection lost: %w", err)
}

// bridgeVNCClient bridges a single VNC client TCP connection to the persistent WebSocket session.
// It returns when the TCP client disconnects or ctx is canceled.
func (o *AppConsoleOptions) bridgeVNCClient(ctx context.Context, tcpConn net.Conn, wsConn *websocket.Conn, wsRecvCh <-chan []byte) {
	clientCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Close the TCP connection when the client context is done to unblock any blocked reads.
	go func() {
		<-clientCtx.Done()
		tcpConn.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// TCP client → WebSocket
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := tcpConn.Read(buf)
			if n > 0 {
				if derr := wsConn.SetWriteDeadline(time.Now().Add(vncWSWriteTimeout)); derr != nil {
					return
				}
				if werr := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket → TCP client
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			select {
			case data, ok := <-wsRecvCh:
				if !ok {
					return
				}
				if _, err := tcpConn.Write(data); err != nil {
					return
				}
			case <-clientCtx.Done():
				return
			}
		}
	}()

	wg.Wait()
}
