package protocol

import "time"

// Resource budgets for the link.
//
// Every value here is a finite, positive implementation constant compiled into
// the binary. None is a service configuration key, none is negotiated on the
// wire, and none may be quoted to a user as a tunable. They are sizing
// decisions, not contract: the contract is only that each is finite and that
// the two inequalities below hold.
const (
	// MaxMessageBytes is the link-scoped read guard passed to SetReadLimit.
	// Exceeding it closes the connection with CloseMessageTooBig and fails
	// every request on the link -- it is a defect to alarm on, never routine
	// backpressure. The in-repo precedent is the application-console route's
	// 1 MiB limit.
	MaxMessageBytes int64 = 1 << 20 // 1 MiB

	// MaxComposedMessageBytes is the bound each sender applies when building a
	// message. Exceeding it is a sender-side defect, caught before the write.
	MaxComposedMessageBytes = 256 << 10 // 256 KiB
)

// Both ends of this link are built from these same constants, so the two
// inequalities the profile requires hold symmetrically and in both directions:
//
//	MaxComposedMessageBytes (256 KiB) < MaxMessageBytes (1 MiB)
//
// that is, each endpoint's read limit exceeds the largest message its peer may
// compose, and each sender's composing bound stays below the limit it knows
// its peer enforces. An implementation that cannot state both has not
// implemented this profile.

// Per-member length bounds. Every member is bounded, not only the payload.
//
// Two consequences are requirements rather than conveniences. A request whose
// id or method fills the read limit with one huge literal is rejected on these
// bounds before any reply is composed, so an oversized input can never force
// an oversized MethodNotSupported response. And a responder never echoes
// unbounded input: a diagnostic message naming a rejected method is truncated
// to MaxStatusMessageLen before it is built.
const (
	MaxIDLen            = 128
	MaxMethodLen        = 128
	MaxReasonLen        = 64
	MaxStatusMessageLen = 1024
	MaxTraceMemberLen   = 512
	MaxTraceMembers     = 2
	MaxNestingDepth     = 32
	// MaxIntegerDigits bounds an integer lexeme before conversion. 19 digits
	// is the width of math.MaxInt64.
	MaxIntegerDigits = 19
)

// Link liveness and write timing.
//
// The keepalive is driven by elapsed time on the connection, never by
// application data: an indefinitely quiet session stays alive and is never
// ended for being quiet. No global read deadline is ever armed -- the library
// documents that a timed-out read corrupts the connection, so a deadline
// intended to bound one request would destroy the link and every sibling.
const (
	// PingInterval is how often the link supervisor sends a Ping.
	PingInterval = 30 * time.Second
	// PongWait bounds how long the supervisor waits for the matching Pong.
	// The wait is armed from the successful return of WriteControl, never
	// from a merely queued Ping: a control write that fails or times out is a
	// socket fault, not a missing Pong.
	PongWait = 30 * time.Second
	// ControlWriteTimeout bounds a control-frame write. Control frames are
	// capped at 125 payload bytes, so this is a bounded pause and never a
	// wait on application work.
	ControlWriteTimeout = 10 * time.Second
	// WriteTimeout bounds a data-message write. An expired write deadline
	// poisons the connection, so it is treated as an unrecoverable
	// link-scoped fault, never as a per-request failure.
	WriteTimeout = 30 * time.Second
)

// Queue capacities. The writer queue is shared; per-request output queues are
// owned by their workers.
const (
	// WriterQueueCapacity bounds messages queued for the single writer.
	WriterQueueCapacity = 64
	// ControlQueueCapacity bounds the per-link control queue. A control
	// action is what frees resources or stops a source, so it has nowhere
	// safe to shed to: an enqueue that fails after the per-request
	// reservation is an unrecoverable link-scoped fault.
	ControlQueueCapacity = 64
	// RecentlyCompletedCapacity bounds the set of finished identifiers kept
	// for stale-duplicate detection. A request naming an evicted identifier
	// is indistinguishable from a new one; that residual window is recorded
	// rather than denied.
	RecentlyCompletedCapacity = 1024
)

// Reconnect backoff. Exponential with full jitter: the delay before attempt n
// is drawn uniformly from [0, min(ReconnectBackoffCap, ReconnectBackoffBase *
// 2^n)), so the first retry is drawn uniformly from 0-30s.
const (
	ReconnectBackoffBase = 30 * time.Second
	ReconnectBackoffCap  = 10 * time.Minute
)

// HandshakeTimeout bounds connection establishment only. After a successful
// dial the library clears the deadline it set, so the link's lifetime is owned
// by the supervisor loop and not by the dial context.
const HandshakeTimeout = 30 * time.Second

// ShutdownBudget bounds step 3 of the ordered shutdown sequence: finishing or
// failing each running request while the connection is still writable. A
// finite request that completes within it sends its own OK; everything else is
// cancelled with an explicit non-OK result for its own id.
const ShutdownBudget = 10 * time.Second
