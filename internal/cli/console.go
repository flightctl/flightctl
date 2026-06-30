package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/gorilla/websocket"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
	"k8s.io/apimachinery/pkg/util/httpstream"
	api_remotecommand "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/exec"
	k8sTerm "k8s.io/kubectl/pkg/util/term"
)

type ConsoleOptions struct {
	GlobalOptions
	tty        bool
	noTTY      bool
	remoteTTY  bool
	protocols  []string
	appName    string
	remoteType string
}

func DefaultConsoleOptions() *ConsoleOptions {
	return &ConsoleOptions{
		GlobalOptions: DefaultGlobalOptions(),
		protocols: []string{
			api_remotecommand.StreamProtocolV5Name,
		},
	}
}

func NewConsoleCmd() *cobra.Command {
	o := DefaultConsoleOptions()

	cmd := &cobra.Command{
		Use:   "console device/NAME [--app APP --remote-type TYPE] [-- COMMAND [ARG...]]",
		Short: "Connect a console to the remote device or to a VM application through the server.",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{DeviceKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Split args at "--"
			var flagArgs, passThroughArgs []string
			for i, arg := range args {
				if arg == "--" {
					flagArgs = args[:i]
					passThroughArgs = args[i+1:]
					break
				}
			}

			// If no "--" was found, all args are flag arguments
			if flagArgs == nil {
				flagArgs = args
			}
			for _, flag := range flagArgs {
				if lo.Contains([]string{"-h", "--help"}, flag) {
					return cmd.Help()
				}
			}

			// Manually parse only the flag arguments
			if err := cmd.Flags().Parse(flagArgs); err != nil {
				return err
			}

			cleanArgs := cmd.Flags().Args()
			// Process console command normally
			if err := o.Complete(cmd, cleanArgs); err != nil {
				return err
			}
			if err := o.Validate(cleanArgs); err != nil {
				return err
			}

			// Run the main console command
			return o.Run(cmd.Context(), cleanArgs, passThroughArgs)
		},
		SilenceUsage:       true,
		DisableFlagParsing: true,
	}

	o.Bind(cmd.Flags())

	return cmd
}

func (o *ConsoleOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.BoolVarP(&o.tty, "tty", "", o.tty, "Allocate remote pseudo terminal")
	fs.BoolVarP(&o.noTTY, "notty", "", o.noTTY, "Don't allocate remote pseudo terminal")
	fs.StringVar(&o.appName, "app", o.appName, "Application name to open a console for (VM serial console)")
	fs.StringVar(&o.remoteType, "remote-type", o.remoteType, "Remote access type when --app is set (e.g. serial)")
}

func (o *ConsoleOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *ConsoleOptions) Validate(args []string) error {
	if len(args) > 2 {
		return fmt.Errorf("arguments must be of the form 'device/NAME' or 'device NAME'")
	}
	kind, name, err := parseAndValidateKindNameFromArgsSingle(args)
	if err != nil {
		return err
	}

	if kind != DeviceKind {
		return fmt.Errorf("only devices can be connected to a console")
	}

	if len(name) == 0 || name == "--" {
		return fmt.Errorf("device name is required")
	}

	if o.tty && o.noTTY {
		return fmt.Errorf("--tty and --notty are mutually exclusive")
	}

	if o.remoteType != "" && o.appName == "" {
		return fmt.Errorf("--remote-type requires --app")
	}

	if o.appName != "" && o.remoteType == "" {
		return fmt.Errorf("--remote-type is required when --app is set")
	}

	return nil
}

func (o *ConsoleOptions) Run(ctx context.Context, flagArgs, passThroughArgs []string) error {
	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	_, name, err := parseAndValidateKindNameFromArgsSingle(flagArgs)
	if err != nil {
		return err
	}

	refresher := client.NewAccessTokenRefresher(config, o.ConfigFilePath, 8080)
	refresher.Start(ctx)
	accessToken := refresher.GetAccessToken()

	if o.appName != "" {
		o.analyzeResponseAndExit(ctx, name, o.connectAppViaWS(ctx, config, name, o.appName, accessToken))
	} else {
		o.analyzeResponseAndExit(ctx, name, o.connectViaWS(ctx, config, name, accessToken, passThroughArgs))
	}

	// unreachable
	return nil
}

// NewWebSocketExecClient creates a WebSocketExecutor
func (o *ConsoleOptions) newWebSocketExecClient(url string, restConfig *rest.Config) (remotecommand.Executor, error) {
	// Create WebSocket executor.  In case we want to support multiple version protocols, they should
	// be added here
	exec, err := remotecommand.NewWebSocketExecutorForProtocols(restConfig, "GET", url, o.protocols...)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebSocket executor: %v", err)
	}

	return exec, nil
}

func (o *ConsoleOptions) SetupTTY() k8sTerm.TTY {
	t := k8sTerm.TTY{
		In:  os.Stdin,
		Out: os.Stdout,
	}

	o.remoteTTY = o.tty || t.IsTerminalIn() && t.IsTerminalOut() && !o.noTTY
	t.Raw = o.remoteTTY && t.IsTerminalIn()

	return t
}

func (o *ConsoleOptions) buildURL(baseURL, metadata string) (string, error) {
	// Initialize a URL object
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse base URL %q: %v", baseURL, err)
	}

	// Create query parameters
	query := url.Values{}
	query.Set(api.DeviceQueryConsoleSessionMetadata, metadata)
	query.Set(api.OrganizationIDQueryKey, o.GetEffectiveOrganization())

	// Encode the query parameters and attach them to the URL
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (o *ConsoleOptions) asTerminalSize(size *remotecommand.TerminalSize) *api.TerminalSize {
	if size == nil {
		return nil
	}
	return &api.TerminalSize{
		Width:  size.Width,
		Height: size.Height,
	}
}

func (o *ConsoleOptions) createSessionMetadata(t k8sTerm.TTY, passThroughArgs []string) string {
	metadata := api.DeviceConsoleSessionMetadata{
		InitialDimensions: o.asTerminalSize(t.GetSize()),
	}
	termEnv := os.Getenv("TERM")
	if termEnv != "" {
		metadata.Term = &termEnv
	}
	if len(passThroughArgs) > 0 {
		metadata.Command = &api.DeviceCommand{
			Command: passThroughArgs[0],
			Args:    passThroughArgs[1:],
		}
	}
	metadata.TTY = o.remoteTTY
	b, _ := json.Marshal(&metadata)
	return string(b)
}

type disconnectionState int

const (
	normal disconnectionState = iota
	newline
	tilde
	disconnected
)

type rawReader struct {
	state     disconnectionState
	cancel    context.CancelFunc
	termState **term.State
	onetime   atomic.Bool
}

func newRawReader(cancel context.CancelFunc, termState **term.State) *rawReader {
	return &rawReader{
		cancel:    cancel,
		state:     newline, // treat start-of-session as "after newline" so ~. works immediately
		termState: termState,
	}
}

func (e *rawReader) Read(p []byte) (int, error) {
	// Ensure the terminal is set to raw mode only when Read function is called for the first time.
	// This is to allow the user to use ctrl+C to stop the console before the terminal is connected
	// to the remote.
	if e.onetime.CompareAndSwap(false, true) {
		if e.termState != nil {
			oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
			if err != nil {
				fmt.Fprintf(os.Stderr, "error making terminal raw: %s\n", err)
				e.cancel()
				return 0, err
			}
			*e.termState = oldState
		}
	}
	if e.state == disconnected {
		e.cancel()
		return 0, io.EOF
	}

	// When in tilde state, we have a buffered ~ that was NOT yet forwarded to
	// the remote. Read the next character to resolve the escape sequence.
	if e.state == tilde {
		if len(p) < 2 {
			// No room to prepend ~; flush it and reset state.
			e.state = normal
			p[0] = '~'
			return 1, nil
		}
		n, err := os.Stdin.Read(p[1:])
		if n == 0 {
			return 0, err
		}
		switch p[1] {
		case '.':
			// Escape sequence complete: disconnect without sending anything.
			e.state = disconnected
			e.cancel()
			return 0, nil
		case '~':
			// Double tilde: send one ~ to the remote and stay in tilde state
			// so the second ~ can itself be escaped or trigger another sequence.
			p[0] = '~'
			return 1, err
		default:
			// Not an escape: send the buffered ~ followed by the new character.
			e.state = normal
			p[0] = '~'
			return 1 + n, err
		}
	}

	n, err := os.Stdin.Read(p)
	out := 0
	for i := 0; i < n; i++ {
		b := p[i]
		switch b {
		case '\n', '\r':
			e.state = newline
		case '~':
			if e.state == newline {
				// Hold the ~ without forwarding it; resolve on the next Read.
				e.state = tilde
				return out, nil
			}
			e.state = normal
		default:
			e.state = normal
		}
		p[out] = b
		out++
	}

	return out, err
}

func (o *ConsoleOptions) connectViaWS(ctx context.Context, config *client.Config, deviceName, token string, passThroughArgs []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t := o.SetupTTY()

	options := remotecommand.StreamOptions{
		Stdout: os.Stdout,
		Tty:    o.remoteTTY,
	}
	var oldState *term.State
	if t.Raw {
		options.Stdin = newRawReader(cancel, &oldState)
	} else if !t.IsTerminalIn() {
		options.Stdin = os.Stdin
	}
	if !o.remoteTTY {
		options.Stderr = os.Stderr
	}

	// this call spawns a goroutine to monitor/update the terminal size
	if t.Raw {
		options.TerminalSizeQueue = t.MonitorSize(t.GetSize())
	}

	if t.Raw {
		defer func() {
			if oldState != nil {
				err := term.Restore(int(os.Stdin.Fd()), oldState)
				// reset terminal and clear screen
				if err != nil {
					fmt.Printf("error restoring terminal: %v", err)
				}
			}
		}()
	}

	restConfig := &rest.Config{
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: config.Service.InsecureSkipVerify,
			CertData: config.AuthInfo.ClientCertificateData,
			CAData:   config.Service.CertificateAuthorityData,
		},
	}

	connURL, err := o.buildURL(fmt.Sprintf("%s/ws/v1/devices/%s/console", config.Service.Server, deviceName),
		o.createSessionMetadata(t, passThroughArgs))
	if err != nil {
		return err
	}
	wsClient, err := o.newWebSocketExecClient(connURL, restConfig)
	if err != nil {
		return err
	}
	return wsClient.StreamWithContext(ctx, options)
}

// buildAppConsoleURL constructs the WebSocket URL for the VM application serial console.
// The base URL's scheme is converted from https/http to wss/ws as required by gorilla/websocket.
func (o *ConsoleOptions) buildAppConsoleURL(consoleServer, deviceName, appName string) (string, error) {
	u, err := url.Parse(fmt.Sprintf("%s/ws/v1/devices/%s/applications/%s/console", consoleServer, deviceName, appName))
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
	q.Set("consoleType", o.remoteType)
	q.Set(api.OrganizationIDQueryKey, o.GetEffectiveOrganization())
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
func (o *ConsoleOptions) connectAppViaWS(ctx context.Context, config *client.Config, deviceName, appName, token string) error {
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
			body, _ := io.ReadAll(resp.Body)
			msg := strings.TrimSpace(string(body))
			return &httpstream.UpgradeFailureError{
				Cause: fmt.Errorf("websocket: bad handshake (%d %s): %s", resp.StatusCode, http.StatusText(resp.StatusCode), msg),
			}
		}
		return err
	}
	defer conn.Close()

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
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			if msgType == websocket.BinaryMessage || msgType == websocket.TextMessage {
				os.Stdout.Write(msg) //nolint:errcheck
			}
		}
	}()

	select {
	case <-done:
	case <-recvDone:
	case <-ctx.Done():
	}

	return ctx.Err()
}

func (o *ConsoleOptions) emitUpgradeFailureError(ctx context.Context, name string, origErr error) {
	// Try to parse error message for HTTP status code and message
	// Format: "websocket: bad handshake (409 Conflict): Device is decommissioned"
	errStr := origErr.Error()
	if strings.Contains(errStr, "bad handshake") {
		// Extract status code and message from error string
		re, err := regexp.Compile(`\((\d+)\s+([^)]+)\):\s*(.+)`)
		if err == nil {
			matches := re.FindStringSubmatch(errStr)
			if len(matches) == 4 {
				statusCode := matches[1]
				statusText := matches[2]
				message := matches[3]
				// For known status codes, display a clean error message
				if statusCode == "409" || statusCode == "401" || statusCode == "403" || statusCode == "404" || statusCode == "503" {
					fmt.Fprintf(os.Stderr, "Error for device %s: %s\n", name, message)
					return
				}
				// For other status codes, still show a cleaner message
				fmt.Fprintf(os.Stderr, "Error for device %s (%s %s): %s\n", name, statusCode, statusText, message)
				return
			}
		}
	}

	// Fallback: try to get device to extract better error message
	c, err := o.BuildClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error for device %s: %v\n", name, origErr)
		return
	}
	c.Start(ctx)
	defer c.Stop()
	response, err := c.GetDeviceWithResponse(ctx, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error for device %s: %v\n", name, origErr)
		return
	}
	if response != nil && response.StatusCode() != http.StatusOK {
		var status *api.Status
		switch {
		case response.JSON401 != nil:
			status = response.JSON401
		case response.JSON403 != nil:
			status = response.JSON403
		case response.JSON404 != nil:
			status = response.JSON404
		case response.JSON503 != nil:
			status = response.JSON503
		}
		if status != nil {
			fmt.Fprintf(os.Stderr, "Error for device %s: %s\n", name, status.Message)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "Error for device %s: %v\n", name, origErr)
}

func (o *ConsoleOptions) analyzeResponseAndExit(ctx context.Context, name string, err error) {
	var exitCode int
	if errors.Is(err, context.Canceled) {
		// If the context was canceled, we exit with code 130 (SIGINT)
		exitCode = 130
	} else {
		switch concreteErr := err.(type) {
		case nil:
		case exec.CodeExitError:
			exitCode = concreteErr.Code
		case *httpstream.UpgradeFailureError:
			exitCode = 255
			o.emitUpgradeFailureError(ctx, name, err)
		default:
			exitCode = 255
			fmt.Fprintf(os.Stderr, "Unexpected error type %T: %+v\n", err, err)
		}
	}
	os.Exit(exitCode)
}
