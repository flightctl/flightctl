// Package protocol implements the agent link wire contract: the
// flightctl.agentlink.v1 subprotocol, its JSON envelope, and the strict
// decoder that envelope requires.
//
// The package is pure: it performs no I/O, owns no goroutines, and is shared
// verbatim by the service-side route and the agent-side link manager so both
// ends enforce one contract rather than two similar ones.
package protocol

// Subprotocol is the WebSocket subprotocol token that selects this envelope
// family and fixes its version. Both ends verify it: the service before
// upgrading, the agent by reading Conn.Subprotocol() after a successful dial.
const Subprotocol = "flightctl.agentlink.v1"

// Version is the only envelope version valid on Subprotocol. Every message in
// both directions carries it as "v"; any other value is a link-scoped
// violation, because a receiver has no agreed way to interpret the rest of the
// object -- including its id.
const Version int64 = 1

// Kind discriminates the four message types. Unlike JSON-RPC there is no
// request/response pair: a request is followed by zero or more data messages
// and exactly one terminal result.
type Kind string

const (
	KindRequest Kind = "request"
	KindData    Kind = "data"
	KindResult  Kind = "result"
	KindCancel  Kind = "cancel"
)

// Origin names the identifier namespace an id belongs to -- which side
// originated the request, not which side sent this message.
//
// Origin is a routing key and nothing else. It is never an authentication
// claim, never a privilege, and never a device or organization assertion; all
// of those come from the verified client certificate. A pair unknown in the
// named namespace is unknown and is never retried in the other.
type Origin string

const (
	OriginService Origin = "service"
	OriginAgent   Origin = "agent"
)

// Opposite returns the endpoint role that is not o. A data or result message
// must come from the endpoint opposite the one origin names; a request or
// cancel must come from the endpoint origin names.
func (o Origin) Opposite() Origin {
	if o == OriginService {
		return OriginAgent
	}
	return OriginService
}

// Status reason strings. These are the only reasons this profile defines; an
// unrecognised reason on a received result is a request-scoped violation
// reported as ReasonRetrievalFailed, never read as success.
const (
	// ReasonOK is normal completion. It is sent after any final data message
	// for the same request and leaves the link open.
	ReasonOK = "OK"
	// ReasonCancelled confirms that a cancelled request stopped its work. It
	// is cleanup accounting: the originator fixed the outcome when it
	// cancelled and does not wait for this.
	ReasonCancelled = "Cancelled"
	// ReasonMethodNotSupported answers a well-formed request naming a method
	// this responder does not implement. It starts no work and leaves the link
	// and siblings untouched. It carries no version verdict.
	ReasonMethodNotSupported = "MethodNotSupported"
	// ReasonRetrievalFailed is the generic request-scoped failure.
	ReasonRetrievalFailed = "RetrievalFailed"
)

// IsValidReason reports whether reason is one this profile defines. A missing,
// empty or unrecognised reason on a received result is itself a request-scoped
// protocol violation.
func IsValidReason(reason string) bool {
	switch reason {
	case ReasonOK, ReasonCancelled, ReasonMethodNotSupported, ReasonRetrievalFailed:
		return true
	default:
		return false
	}
}

// IsTerminalKind reports whether k fixes a request's outcome.
func IsTerminalKind(k Kind) bool { return k == KindResult }
