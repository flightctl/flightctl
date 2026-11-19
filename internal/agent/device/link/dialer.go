package link

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"strings"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/gorilla/websocket"
)

// AgentLinkPath is the route the agent dials. It mirrors the service-side
// constant; the two are kept in step by the handshake itself, which fails
// visibly if they diverge.
const AgentLinkPath = "/ws/v1/agent/link"

// newDialer builds the link's own websocket.Dialer.
//
// It is deliberately not websocket.DefaultDialer. DefaultDialer sets Proxy to
// http.ProxyFromEnvironment, and a dialer whose Proxy is nil uses no proxy at
// all. Leaving it explicitly nil is what makes this a direct dial that the
// agent process's environment cannot redirect. This concerns only this
// dialer: it changes no other flightctl client's proxy handling.
func newDialer(tlsConfig *tls.Config) *websocket.Dialer {
	return &websocket.Dialer{
		// Explicitly nil: no proxy, ever, on this dial.
		Proxy:            nil,
		TLSClientConfig:  tlsConfig,
		HandshakeTimeout: protocol.HandshakeTimeout,
		Subprotocols:     []string{protocol.Subprotocol},
		// NetDialTLSContext is deliberately left unset. If it were set the
		// library would assume the TLS handshake is done there and ignore
		// TLSClientConfig entirely, obliging this code to reapply RootCAs,
		// ServerName, MinVersion and the client certificate by hand. Both
		// identity paths supply a tls.Config instead -- the TPM-backed one
		// through a certificate whose private key is the TPM signer -- so
		// both reach the same verification outcome through the same code.
		NetDialTLSContext: nil,
	}
}

// linkTLSConfig adapts an identity's verification policy for this dial.
//
// The only change is the ALPN list. The library clones the config it is given
// and reports an explicit failure when the handshake negotiates a protocol
// other than http/1.1, so reusing the gRPC transport credentials' config --
// which advertises h2 -- would break the dial. CA and certificate material is
// shared; the ALPN list is not.
//
// No variant of this may weaken the policy: it must not set
// InsecureSkipVerify, substitute a trust store, rewrite ServerName or change
// the backend address. The link adds no new way to disable verification.
func linkTLSConfig(base *tls.Config) *tls.Config {
	cfg := base.Clone()
	cfg.NextProtos = []string{"http/1.1"}
	return cfg
}

// linkURL turns the management service endpoint into the wss URL of the
// agent link route.
//
// The scheme is wss only. There is no ws form and no plaintext listener: the
// link is dialled over the same TLS 1.3 mutual handshake the agent already
// uses for every other call to this endpoint.
func linkURL(config *baseclient.Config) (string, error) {
	server := strings.TrimSpace(config.Service.Server)
	if server == "" {
		return "", fmt.Errorf("management service has no server address")
	}

	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("parsing server URL %q: %w", server, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server URL %q has no host", server)
	}

	return (&url.URL{Scheme: "wss", Host: u.Host, Path: AgentLinkPath}).String(), nil
}
