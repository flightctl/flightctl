package agentserver

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/agentlink"
	"github.com/flightctl/flightctl/internal/agentlink/link"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// AgentLinkPath is the agent link route.
//
// It is mounted on the existing agent HTTP router, so it inherits that
// router's authentication chain, but outside the /api/v1 subtree so the
// OpenAPI validator -- which would reject a path absent from the agent spec
// -- does not run on it.
const AgentLinkPath = "/ws/v1/agent/link"

// agentLinkHandler serves the agent link route.
type agentLinkHandler struct {
	registry  *agentlink.Registry
	serverCtx context.Context
	log       logrus.FieldLogger
	upgrader  websocket.Upgrader
}

func newAgentLinkHandler(registry *agentlink.Registry, serverCtx context.Context, log logrus.FieldLogger) *agentLinkHandler {
	return &agentLinkHandler{
		registry:  registry,
		serverCtx: serverCtx,
		log:       log,
		upgrader: websocket.Upgrader{
			Subprotocols: []string{protocol.Subprotocol},
			// This route rejects any request carrying an Origin header at
			// all, replacing the library's default. Origin is an arbitrary
			// client-supplied header: it is neither proof of client type nor
			// an identity, its absence does not establish that a peer is not
			// a browser, and it never grants a permission. The refusal is a
			// profile policy and defence in depth -- the gate that actually
			// admits the link is the verified mTLS client certificate and the
			// agent-identity and organization authorization that complete
			// before the 101. The application-console route returns true
			// unconditionally; that is a user-facing route with a different
			// threat model and is deliberately not copied here.
			CheckOrigin: func(r *http.Request) bool {
				return r.Header.Get("Origin") == ""
			},
		},
	}
}

// ServeHTTP completes the upgrade and runs the link.
//
// Every refusal below happens before the 101 is written. Once the upgrade
// succeeds there is no further HTTP status, ever: failures are per-request
// result messages or a Close frame with a WebSocket code.
func (h *agentLinkHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Hijacking requires HTTP/1.1: the library obtains the raw connection
	// through http.Hijacker, and Go's HTTP/2 ResponseWriter is not one.
	// Because the multiplexer sends a non-gRPC HTTP/2 request to this same
	// handler, the route checks the version itself and answers with 426
	// rather than an opaque 500.
	if r.ProtoMajor != 1 {
		w.Header().Set("Upgrade", "HTTP/1.1")
		http.Error(w, "agent link requires HTTP/1.1", http.StatusUpgradeRequired)
		return
	}

	if r.Header.Get("Origin") != "" {
		http.Error(w, "Origin header is not accepted on this route", http.StatusForbidden)
		return
	}

	// The library negotiates a subprotocol by first match and simply omits
	// the response header when nothing matches, which would leave the two
	// sides silently disagreeing. The offer is therefore checked before
	// upgrading.
	if !offersSubprotocol(r, protocol.Subprotocol) {
		http.Error(w, fmt.Sprintf("subprotocol %s must be offered", protocol.Subprotocol), http.StatusBadRequest)
		return
	}

	// The identity was established by the middleware chain before this
	// handler ran, and is read only from the verified client certificate.
	// Nothing in the HTTP request and nothing in any JSON message may set or
	// override it.
	deviceName, orgID, err := agentLinkIdentity(r.Context())
	if err != nil {
		h.log.Warnf("Agent link: refusing upgrade: %v", err)
		http.Error(w, "agent identity could not be established", http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written the error response.
		h.log.Debugf("Agent link: upgrade failed for device %s: %v", deviceName, err)
		return
	}

	if conn.Subprotocol() != protocol.Subprotocol {
		h.log.Errorf("Agent link: negotiated subprotocol %q is not %q", conn.Subprotocol(), protocol.Subprotocol)
		_ = conn.Close()
		return
	}

	// Go cancels a request's context when the handler returns, hijacked
	// connections included. The verified device and organization are
	// therefore copied here. The link runs under the server lifetime so
	// server shutdown cancels active links before their KV dependency is
	// closed.
	linkCtx, cancel := context.WithCancel(h.serverCtx)
	defer cancel()

	h.runLink(linkCtx, conn, deviceName, orgID)
}

// runLink registers the connection and serves it until it ends.
func (h *agentLinkHandler) runLink(ctx context.Context, conn *websocket.Conn, deviceName string, orgID uuid.UUID) {
	linkID := uuid.NewString()
	logger := h.log.WithField("device", deviceName).WithField("linkId", linkID)

	lease, err := h.registry.Register(ctx, orgID, deviceName, linkID)
	if err != nil {
		// Without a registration the link cannot be dispatched to, so there
		// is nothing useful it could serve.
		logger.Errorf("Agent link: failed to register link: %v", err)
		_ = conn.Close()
		return
	}
	logger.Infof("Agent link established")

	go lease.KeepAlive(ctx)
	defer func() {
		// Releasing compares the stored value, so a link that has already
		// been superseded removes nothing and cannot delete its
		// replacement's registration.
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), agentlink.DispatchRefreshInterval)
		defer releaseCancel()
		if err := lease.Release(releaseCtx); err != nil {
			logger.Warnf("Agent link: failed to release registration: %v", err)
		}
	}()

	err = link.Serve(ctx, conn, link.Config{
		Role: protocol.OriginService,
		// No method is implemented on the service side yet: the link carries
		// the envelope and nothing that uses it, so a well-formed
		// agent-originated request is answered with one MethodNotSupported.
		Handlers: nil,
		Log:      logger,
	})
	logger.Infof("Agent link closed: %v", err)
}

// offersSubprotocol reports whether the handshake offered a token, using the
// library's own parse of Sec-WebSocket-Protocol.
func offersSubprotocol(r *http.Request, want string) bool {
	for _, offered := range websocket.Subprotocols(r) {
		if offered == want {
			return true
		}
	}
	return false
}

// agentLinkIdentity reads the verified device and organization that the
// agent middleware chain placed in the request context.
func agentLinkIdentity(ctx context.Context) (string, uuid.UUID, error) {
	val := ctx.Value(consts.IdentityCtxKey)
	if val == nil {
		return "", uuid.Nil, fmt.Errorf("no identity in request context")
	}
	identity, ok := val.(*fcmiddleware.AgentIdentity)
	if !ok {
		return "", uuid.Nil, fmt.Errorf("identity in request context is not an agent identity")
	}
	// For agents this is the device fingerprint, which is the device name.
	deviceName := identity.GetUsername()
	if deviceName == "" {
		return "", uuid.Nil, fmt.Errorf("agent identity carries no device name")
	}

	orgID, ok := util.GetOrgIdFromContext(ctx)
	if !ok {
		return "", uuid.Nil, fmt.Errorf("no organization in request context")
	}

	return deviceName, orgID, nil
}
