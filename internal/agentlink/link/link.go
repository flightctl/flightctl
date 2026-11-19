// Package link implements the live agent link: the reader, writer and
// supervisor that run over one established WebSocket connection, the
// attribution rules that decide what each received message may do, and the
// ordered shutdown sequence.
//
// Both ends run this same machinery. The service mounts it behind the agent
// link route; the agent runs it under a manager that dials and redials. The
// structure is symmetric because the contract is: after the 101 either peer
// may send at any time, and the envelope lets either endpoint originate a
// request.
package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// Handler runs one request to a terminal result.
//
// A handler owns its request and nothing else. It must not touch the link,
// must not observe or disturb a sibling request, and must return when its
// context is cancelled -- cancellation means the originator has already fixed
// this request's outcome and released its resources.
//
// If a handler returns without fixing an outcome, the endpoint sends
// RetrievalFailed for it: one result each, never none.
type Handler func(ctx context.Context, req *Request, out Responder)

// Request is an accepted peer-originated request.
type Request struct {
	Origin protocol.Origin
	ID     string
	Method string
	// Params is the request's parameter object, validated only as an object.
	// A method decodes it against its own schema after it has been resolved.
	Params *protocol.Value
	// Trace carries W3C trace context for this request. It is per request
	// because a request arriving long after the connection opened cannot
	// inherit anything from the handshake.
	Trace map[string]string
}

// Responder is how a handler emits output for its own request.
type Responder interface {
	// Data enqueues one data message for this request.
	Data(payload json.RawMessage) error
	// Result fixes this request's outcome. Only the first call takes effect;
	// later calls are ignored, because a request's outcome is fixed exactly
	// once and later events never overwrite it.
	Result(reason, message string) error
}

// Config describes one endpoint.
type Config struct {
	// Role is which endpoint this side is. It decides which identifier
	// namespace this side allocates into and which sender role each received
	// kind requires.
	Role protocol.Origin

	// Handlers is the closed, compiled-in allowlist of methods this endpoint
	// implements in the responder role. A method absent from it is answered
	// with exactly one MethodNotSupported result and starts no work.
	//
	// It is deliberately empty in the current build: the link carries no
	// method yet, so every well-formed request is refused that way. Adding a
	// method is not an envelope change and involves no version bump.
	Handlers map[string]Handler

	Log logrus.FieldLogger

	// ShutdownBudget bounds how long the ordered shutdown waits for running
	// requests to finish on their own before cancelling them. Zero selects
	// protocol.ShutdownBudget.
	ShutdownBudget time.Duration

	// OnConnect, if set, is called with the live endpoint once it is ready
	// and before the reader starts, and is how a caller reaches Originate
	// to send a request of its own.
	//
	// It runs on Serve's goroutine, so it must not block: anything
	// long-running belongs on a goroutine the callback starts. The endpoint
	// stops accepting work once Serve returns.
	OnConnect func(*Endpoint)
}

type requestKey struct {
	origin protocol.Origin
	id     string
}

func (k requestKey) String() string { return string(k.origin) + "/" + k.id }

// Counters records messages that were dropped rather than acted on. A drop is
// silent on the wire -- it is never answered with a result, a cancel or an
// error, which is what makes an error loop between two peers structurally
// impossible -- so these counters are the only evidence it happened.
type Counters struct {
	DroppedUnknownPair    atomic.Uint64
	DroppedStaleDuplicate atomic.Uint64
	DroppedRoleViolation  atomic.Uint64
	RequestViolations     atomic.Uint64
	MethodNotSupported    atomic.Uint64
}

type requestState struct {
	cancel context.CancelFunc
	done   chan struct{}
	// resultSent guards the one-result-each rule.
	resultSent atomic.Bool
}

type outbound struct {
	data []byte
	// flushed, when set, is closed once the writer has processed this item.
	// Because the writer is single and serves the queue in order, that also
	// means everything queued before it has reached the socket. It is how
	// shutdown knows the Close frame really is last.
	flushed chan struct{}
}

// Endpoint is one side of a live link.
type Endpoint struct {
	ws   *websocket.Conn
	role protocol.Origin
	log  logrus.FieldLogger

	handlers       map[string]Handler
	counters       Counters
	shutdownBudget time.Duration

	// linkCtx governs work running on this link. It is deliberately not
	// derived from the caller's context: shutdown is ordered, and cancelling
	// every request before its result could be written would make the
	// "finish or fail each running request while the connection is still
	// writable" step impossible.
	linkCtx    context.Context
	linkCancel context.CancelFunc

	writeCh chan outbound

	mu sync.Mutex
	// active holds peer-originated requests this endpoint is responding to.
	active map[requestKey]*requestState
	// originated holds requests this endpoint originated and still has
	// outstanding, against which an incoming data or result is matched.
	originated map[requestKey]*Originated
	// completed is the bounded recently-completed set. A request naming an
	// identifier still in it is a stale duplicate; one naming an evicted
	// identifier is indistinguishable from a new request and will start one.
	// That residual window is recorded rather than denied.
	completed *ringSet
	// refusing is shutdown step 2: new starts are refused explicitly, never
	// silently ignored and never left running orphaned.
	refusing bool

	pongCh chan struct{}

	closeOnce sync.Once
	closeErr  error
	// readerExited is closed when the read loop returns, whatever the cause.
	readerExited chan struct{}
}

// Serve runs the link over an established connection until it ends, and
// returns the reason it ended.
//
// Cancelling ctx begins the ordered shutdown sequence rather than tearing the
// connection down: stop accepting new starts, finish or fail each running
// request while the connection is still writable, then send the Close frame.
// Closing first is impossible, because the link is the only way to deliver a
// terminal result to the requests still running on it.
//
// Serve always closes ws before returning.
func Serve(ctx context.Context, ws *websocket.Conn, cfg Config) error {
	if cfg.Log == nil {
		return errors.New("link: a logger is required")
	}
	if cfg.Role != protocol.OriginService && cfg.Role != protocol.OriginAgent {
		return fmt.Errorf("link: invalid role %q", cfg.Role)
	}

	e := &Endpoint{
		ws:             ws,
		role:           cfg.Role,
		log:            cfg.Log,
		handlers:       cfg.Handlers,
		shutdownBudget: cfg.ShutdownBudget,
		writeCh:        make(chan outbound, protocol.WriterQueueCapacity),
		active:         map[requestKey]*requestState{},
		originated:     map[requestKey]*Originated{},
		completed:      newRingSet(protocol.RecentlyCompletedCapacity),
		pongCh:         make(chan struct{}, 1),
		readerExited:   make(chan struct{}),
	}
	if e.shutdownBudget <= 0 {
		e.shutdownBudget = protocol.ShutdownBudget
	}
	e.linkCtx, e.linkCancel = context.WithCancel(context.WithoutCancel(ctx))
	defer e.linkCancel()

	// The read limit is a finite link-scoped guard. Exceeding it closes the
	// connection with 1009 and fails every request on it, so it is a defect
	// to alarm on rather than routine backpressure.
	ws.SetReadLimit(protocol.MaxMessageBytes)

	// No global read deadline is ever armed: the library documents that after
	// a read has timed out the connection state is corrupt and all future
	// reads fail, so a deadline meant to bound one request would destroy the
	// link and every sibling. Liveness is the supervisor's job instead.

	// The Pong handler is installed before the read loop starts. It records
	// that a Pong arrived and returns: non-blocking, and it never writes.
	ws.SetPongHandler(func(string) error {
		select {
		case e.pongCh <- struct{}{}:
		default:
		}
		return nil
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.runWriter() }()
	go func() { defer wg.Done(); e.runSupervisor() }()

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			e.shutdown()
		case <-e.readerExited:
		}
	}()

	if cfg.OnConnect != nil {
		cfg.OnConnect(e)
	}

	readErr := e.runReader()
	close(e.readerExited)

	// The link has ended. Every request still running on it fails explicitly
	// and individually -- a Close from either side including 1000, a
	// transport error and a reset differ in diagnostics and nothing else.
	e.failAllActive()
	e.linkCancel()
	_ = ws.Close()

	<-shutdownDone
	wg.Wait()
	return readErr
}

// Counters returns the endpoint's drop counters.
func (e *Endpoint) Counters() *Counters { return &e.counters }

// peerRole is the role the endpoint on the other end of this connection
// plays. Every received message is checked against it.
func (e *Endpoint) peerRole() protocol.Origin { return e.role.Opposite() }
