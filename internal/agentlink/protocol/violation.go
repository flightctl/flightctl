package protocol

import (
	"fmt"

	"github.com/gorilla/websocket"
)

// Attributable faults fail one request; unattributable faults fail the link.
// Nothing in between silently continues, so every rejection this package
// produces is exactly one of the two types below.

// LinkViolation is a fault that cannot be attributed to a single request. The
// receiver closes the connection with CloseCode and fails every request on
// it. The close code is a diagnostic about the connection and is never
// rendered as a request's status.
type LinkViolation struct {
	// CloseCode is the WebSocket close code to send. The complete set this
	// profile sends is 1000, 1001, 1002, 1007, 1008, 1009 and 1011.
	CloseCode int
	// Text is the close-frame reason. WebSocket control frames are capped at
	// 125 payload bytes, so it is kept short and never echoes peer input.
	Text string
	Err  error
}

func (e *LinkViolation) Error() string {
	return fmt.Sprintf("link violation (close %d): %v", e.CloseCode, e.Err)
}

func (e *LinkViolation) Unwrap() error { return e.Err }

// RequestViolation is a fault in a message that could be attributed. Exactly
// one request fails; the link and its siblings continue.
type RequestViolation struct {
	Origin Origin
	ID     string
	// Kind is the kind of the offending message. It decides whether the
	// receiver answers: a malformed request is answered with one result, so
	// its originator is not left waiting, while a malformed data, result or
	// cancel is dropped and counted and never answered.
	Kind Kind
	// Reason is the status reason to report, always one this profile defines.
	Reason string
	Err    error
}

func (e *RequestViolation) Error() string {
	return fmt.Sprintf("request violation (%s/%s, %s): %v", e.Origin, e.ID, e.Reason, e.Err)
}

func (e *RequestViolation) Unwrap() error { return e.Err }

func linkFault(code int, text string, format string, args ...any) *LinkViolation {
	return &LinkViolation{CloseCode: code, Text: text, Err: fmt.Errorf(format, args...)}
}

// protocolFault is the common link-scoped case: a message that is not in the
// negotiated profile at all, closed with 1002.
func protocolFault(format string, args ...any) *LinkViolation {
	return linkFault(websocket.CloseProtocolError, "protocol error", format, args...)
}

func requestFault(env *Envelope, format string, args ...any) *RequestViolation {
	return &RequestViolation{
		Origin: env.Origin,
		ID:     env.ID,
		Kind:   env.Kind,
		Reason: ReasonRetrievalFailed,
		Err:    fmt.Errorf(format, args...),
	}
}
