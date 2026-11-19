package link

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// The endpoint under test plays the service role, and the test drives the
// other end as the agent. So a request the test sends names origin "agent",
// and the data and result it expects back name that same origin.

const testTimeout = 5 * time.Second

// peerEvent is one thing the peer observed: a decoded message, or the error
// that ended its read loop.
type peerEvent struct {
	env *protocol.Envelope
	err error
}

type harness struct {
	// peer is the raw connection the test drives. It is deliberately raw so
	// a test can send a message the encoder would refuse to build.
	peer *websocket.Conn
	// events carries what the peer read. A dedicated reader goroutine keeps
	// the peer reading at all times, because no read deadline may ever be
	// armed on a live connection: a timed-out read corrupts the connection
	// and every later read fails.
	events <-chan peerEvent
	// served receives Serve's return value once the link ends.
	served <-chan error
	// stop begins the endpoint's ordered shutdown.
	stop context.CancelFunc
}

func newHarness(t *testing.T, handlers map[string]Handler) *harness {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	served := make(chan error, 1)
	upgrader := websocket.Upgrader{Subprotocols: []string{protocol.Subprotocol}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			served <- err
			return
		}
		served <- Serve(ctx, conn, Config{
			Role:     protocol.OriginService,
			Handlers: handlers,
			Log:      log,
			// Keep the ordered shutdown quick enough to assert on. The
			// sequence is unchanged; only how long it waits for a running
			// request to finish on its own differs.
			ShutdownBudget: 250 * time.Millisecond,
		})
	}))
	t.Cleanup(srv.Close)

	dialer := &websocket.Dialer{Subprotocols: []string{protocol.Subprotocol}}
	peer, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	events := make(chan peerEvent, 32)
	go func() {
		defer close(events)
		for {
			_, data, err := peer.ReadMessage()
			if err != nil {
				events <- peerEvent{err: err}
				return
			}
			env, decodeErr := protocol.Decode(data)
			events <- peerEvent{env: env, err: decodeErr}
		}
	}()

	return &harness{peer: peer, events: events, served: served, stop: cancel}
}

func (h *harness) send(t *testing.T, raw string) {
	t.Helper()
	require.NoError(t, h.peer.WriteMessage(websocket.TextMessage, []byte(raw)))
}

// recv returns the next application message the endpoint sent.
func (h *harness) recv(t *testing.T) *protocol.Envelope {
	t.Helper()
	select {
	case ev, ok := <-h.events:
		require.True(t, ok, "peer read loop ended before a message arrived")
		require.NoError(t, ev.err)
		return ev.env
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for a message")
		return nil
	}
}

// expectClose reads until the endpoint closes and returns the close code.
func (h *harness) expectClose(t *testing.T) int {
	t.Helper()
	for {
		select {
		case ev, ok := <-h.events:
			require.True(t, ok, "peer read loop ended without a close frame")
			if ev.err == nil {
				continue
			}
			var ce *websocket.CloseError
			if errors.As(ev.err, &ce) {
				return ce.Code
			}
			t.Fatalf("expected a close frame, got %v", ev.err)
		case <-time.After(testTimeout):
			t.Fatal("timed out waiting for a close frame")
		}
	}
}

// expectNoMessage asserts the endpoint sends nothing further within a short
// window -- the way a "dropped and counted" message is observed on the wire.
func (h *harness) expectNoMessage(t *testing.T) {
	t.Helper()
	select {
	case ev, ok := <-h.events:
		if !ok {
			t.Fatal("peer read loop ended unexpectedly")
		}
		if ev.err != nil {
			t.Fatalf("expected no message, but the link ended: %v", ev.err)
		}
		t.Fatalf("expected no message, but received a %s for %s", ev.env.Kind, ev.env.ID)
	case <-time.After(250 * time.Millisecond):
	}
}

func TestUnimplementedMethodIsRefusedOnce(t *testing.T) {
	require := require.New(t)
	h := newHarness(t, nil)

	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"nope.not.here","params":{}}`)

	env := h.recv(t)
	require.Equal(protocol.KindResult, env.Kind)
	require.Equal("r1", env.ID)
	require.Equal(protocol.ReasonMethodNotSupported, env.Status.Reason)

	// Exactly one result, and the link and its siblings are untouched.
	h.expectNoMessage(t)
}

func TestRequestForUnimplementedMethodDoesNotInspectParams(t *testing.T) {
	require := require.New(t)
	h := newHarness(t, nil)

	// params carries a shape this endpoint could not validate. Because the
	// method is resolved first, the request is still refused with
	// MethodNotSupported rather than reported as a malformed request.
	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"future.method","params":{"unknownToUs":{"deeply":["nested"]}}}`)

	env := h.recv(t)
	require.Equal(protocol.ReasonMethodNotSupported, env.Status.Reason)
}

func TestHandlerStreamsDataThenResult(t *testing.T) {
	require := require.New(t)

	h := newHarness(t, map[string]Handler{
		"test.echo": func(ctx context.Context, req *Request, out Responder) {
			_ = out.Data(json.RawMessage(`{"content":"one"}`))
			_ = out.Data(json.RawMessage(`{"content":"two"}`))
			_ = out.Result(protocol.ReasonOK, "")
		},
	})

	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"test.echo","params":{}}`)

	first := h.recv(t)
	require.Equal(protocol.KindData, first.Kind)
	second := h.recv(t)
	require.Equal(protocol.KindData, second.Kind)

	// The result arrives behind the request's data, never dispatched around it.
	last := h.recv(t)
	require.Equal(protocol.KindResult, last.Kind)
	require.Equal(protocol.ReasonOK, last.Status.Reason)
}

func TestHandlerReturningWithoutResultStillFixesAnOutcome(t *testing.T) {
	require := require.New(t)

	h := newHarness(t, map[string]Handler{
		"test.silent": func(ctx context.Context, req *Request, out Responder) {},
	})

	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"test.silent","params":{}}`)

	env := h.recv(t)
	require.Equal(protocol.KindResult, env.Kind)
	require.Equal(protocol.ReasonRetrievalFailed, env.Status.Reason)
}

func TestCancelStopsOneRequestAndLeavesTheLinkOpen(t *testing.T) {
	require := require.New(t)

	started := make(chan struct{})
	h := newHarness(t, map[string]Handler{
		"test.block": func(ctx context.Context, req *Request, out Responder) {
			close(started)
			<-ctx.Done()
			_ = out.Result(protocol.ReasonCancelled, "cancelled by peer")
		},
	})

	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"test.block","params":{}}`)
	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatal("handler did not start")
	}

	h.send(t, `{"v":1,"kind":"cancel","origin":"agent","id":"r1"}`)

	env := h.recv(t)
	require.Equal(protocol.ReasonCancelled, env.Status.Reason)

	// The link is still usable: a new request is still answered.
	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r2","method":"absent","params":{}}`)
	next := h.recv(t)
	require.Equal("r2", next.ID)
	require.Equal(protocol.ReasonMethodNotSupported, next.Status.Reason)
}

func TestCancelForUnknownIdentifierIsDroppedIdempotently(t *testing.T) {
	h := newHarness(t, nil)

	h.send(t, `{"v":1,"kind":"cancel","origin":"agent","id":"never-existed"}`)

	// A drop is silent on the wire: never answered with a result, a cancel
	// or an error.
	h.expectNoMessage(t)
}

func TestStaleDuplicateRequestIsDroppedRatherThanAnsweredTwice(t *testing.T) {
	require := require.New(t)
	h := newHarness(t, nil)

	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"absent","params":{}}`)
	first := h.recv(t)
	require.Equal(protocol.ReasonMethodNotSupported, first.Status.Reason)

	// The identifier is now in the recently-completed set.
	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"absent","params":{}}`)
	h.expectNoMessage(t)
}

func TestDuplicateLiveIdentifierFailsTheLink(t *testing.T) {
	require := require.New(t)

	started := make(chan struct{})
	h := newHarness(t, map[string]Handler{
		"test.block": func(ctx context.Context, req *Request, out Responder) {
			close(started)
			<-ctx.Done()
		},
	})

	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"test.block","params":{}}`)
	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatal("handler did not start")
	}

	// No identifier is recycled to resolve the collision: the link fails.
	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"test.block","params":{}}`)

	require.Equal(websocket.ClosePolicyViolation, h.expectClose(t))
}

func TestUnattributableMessageFailsTheLink(t *testing.T) {
	testCases := []struct {
		name      string
		raw       string
		closeCode int
	}{
		{
			name:      "When the envelope version is not 1 it should close with a protocol error",
			raw:       `{"v":2,"kind":"cancel","origin":"agent","id":"r1"}`,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When a routing member is missing it should close with a protocol error",
			raw:       `{"v":1,"kind":"cancel","origin":"agent"}`,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When the message is not a JSON object it should close with a protocol error",
			raw:       `"just a string"`,
			closeCode: websocket.CloseProtocolError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.send(t, tc.raw)
			require.New(t).Equal(tc.closeCode, h.expectClose(t))
		})
	}
}

func TestBinaryMessageFailsTheLink(t *testing.T) {
	require := require.New(t)
	h := newHarness(t, nil)

	require.NoError(h.peer.WriteMessage(websocket.BinaryMessage, []byte("binary is not used")))

	require.Equal(websocket.CloseProtocolError, h.expectClose(t))
}

func TestOversizeMessageFailsTheLinkWithMessageTooBig(t *testing.T) {
	require := require.New(t)
	h := newHarness(t, nil)

	// The read limit is link-scoped: the library sends 1009 and the
	// connection ends, rather than one request failing.
	oversize := strings.Repeat("x", int(protocol.MaxMessageBytes)+1024)
	require.NoError(h.peer.WriteMessage(websocket.TextMessage, []byte(oversize)))

	require.Equal(websocket.CloseMessageTooBig, h.expectClose(t))
}

func TestMessageFromTheWrongSenderRoleIsDropped(t *testing.T) {
	h := newHarness(t, nil)

	// The peer is the agent. A data message naming origin "agent" would have
	// to come from the service, so arriving from the agent it is a role
	// violation: dropped and counted, nothing answered.
	h.send(t, `{"v":1,"kind":"data","origin":"agent","id":"r1","payload":{}}`)

	h.expectNoMessage(t)
}

func TestResultNamingAnUnknownPairIsDropped(t *testing.T) {
	h := newHarness(t, nil)

	// A result naming a service-originated request this endpoint never
	// originated: dropped and counted, never answered, so no error can
	// bounce between peers.
	h.send(t, `{"v":1,"kind":"result","origin":"service","id":"never","status":{"reason":"OK"}}`)

	h.expectNoMessage(t)
}

func TestMalformedRequestIsAnsweredAndLeavesTheLinkOpen(t *testing.T) {
	require := require.New(t)
	h := newHarness(t, nil)

	// An unknown member is attributable, so it fails only this request --
	// and because the offending message is a request, its originator is
	// answered rather than left waiting.
	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"absent","params":{},"surprise":1}`)

	env := h.recv(t)
	require.Equal(protocol.KindResult, env.Kind)
	require.Equal("r1", env.ID)
	require.Equal(protocol.ReasonRetrievalFailed, env.Status.Reason)

	// Exactly one answer, and the link still works.
	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r2","method":"absent","params":{}}`)
	next := h.recv(t)
	require.Equal("r2", next.ID)
	require.Equal(protocol.ReasonMethodNotSupported, next.Status.Reason)
}

func TestMalformedResponderMessageIsNotAnswered(t *testing.T) {
	h := newHarness(t, nil)

	// A malformed result names a request this endpoint never originated.
	// A result is never replied to, which is what makes an error loop
	// between two peers structurally impossible.
	h.send(t, `{"v":1,"kind":"result","origin":"service","id":"r1","status":{"reason":"OK"},"surprise":1}`)

	h.expectNoMessage(t)
}

func TestShutdownFailsRunningRequestsBeforeClosing(t *testing.T) {
	require := require.New(t)

	started := make(chan struct{})
	h := newHarness(t, map[string]Handler{
		"test.follow": func(ctx context.Context, req *Request, out Responder) {
			close(started)
			<-ctx.Done()
			_ = out.Result(protocol.ReasonRetrievalFailed, "endpoint is shutting down")
		},
	})

	h.send(t, `{"v":1,"kind":"request","origin":"agent","id":"r1","method":"test.follow","params":{}}`)
	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatal("handler did not start")
	}

	h.stop()

	// The per-request result arrives before the Close frame: the link is the
	// only way to deliver it, so closing first would lose it.
	env := h.recv(t)
	require.Equal(protocol.KindResult, env.Kind)
	require.Equal("r1", env.ID)
	require.Equal(protocol.ReasonRetrievalFailed, env.Status.Reason)

	require.Equal(websocket.CloseGoingAway, h.expectClose(t))
}

func TestServeReturnsWhenThePeerClosesCleanly(t *testing.T) {
	require := require.New(t)
	h := newHarness(t, nil)

	msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done")
	require.NoError(h.peer.WriteControl(websocket.CloseMessage, msg, time.Now().Add(testTimeout)))

	select {
	case err := <-h.served:
		// A clean 1000 ends the link; it is never a request's success.
		require.Error(err)
	case <-time.After(testTimeout):
		t.Fatal("Serve did not return after a clean close")
	}
}

func TestRingSetEvictsOldestFirst(t *testing.T) {
	require := require.New(t)

	s := newRingSet(2)
	a := requestKey{origin: protocol.OriginAgent, id: "a"}
	b := requestKey{origin: protocol.OriginAgent, id: "b"}
	c := requestKey{origin: protocol.OriginAgent, id: "c"}

	s.add(a)
	s.add(b)
	require.True(s.has(a))
	require.True(s.has(b))

	s.add(c)
	require.Equal(2, s.len())
	require.False(s.has(a), "the oldest identifier should have been evicted")
	require.True(s.has(b))
	require.True(s.has(c))
}

func TestRingSetIgnoresRepeatedAdds(t *testing.T) {
	require := require.New(t)

	s := newRingSet(4)
	k := requestKey{origin: protocol.OriginService, id: "x"}
	s.add(k)
	s.add(k)

	require.Equal(1, s.len())
}
