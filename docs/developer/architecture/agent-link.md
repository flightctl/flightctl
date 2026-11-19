# Agent link

The agent link is a persistent WebSocket connection between a device agent and
`flightctl-api`, carrying a small JSON envelope protocol. The agent opens it on
startup and keeps it open, so work dispatched over it needs no per-request
connection setup.

This document describes the transport and the envelope. **No method is
implemented yet**: the link carries the envelope and nothing that uses it, so
every well-formed request is currently answered with one `MethodNotSupported`.
The agent-side log retrieval the first method will call is built and callable
in isolation; see [Device log retrieval](device-log-retrieval.md).

## Endpoint and handshake

| Property | Value |
|---|---|
| Endpoint | the existing agent-facing listener, `service.agentEndpointAddress` (default `:7443`) |
| Path | `GET /ws/v1/agent/link`, outside the `/api/v1` OpenAPI-validated subtree |
| Scheme | `wss` only; there is no `ws` form and no plaintext listener |
| HTTP version | HTTP/1.1, because the upgrade hijacks the connection |
| Subprotocol | `flightctl.agentlink.v1`, verified by **both** sides |
| Device identity | from the verified client certificate only |

The route is mounted on the agent HTTP router, so it inherits that router's
authentication chain and every refusal happens **before** the `101`:

| Condition | Response |
|---|---|
| Missing, expired or untrusted client certificate | TLS handshake fails; no HTTP status exists |
| Agent authentication or organization check fails | `401` / `403` |
| An `Origin` header is present at all | `403` |
| `flightctl.agentlink.v1` is not offered | `400` |
| The request is not HTTP/1.1 | `426` naming HTTP/1.1 |

After the `101` there is no HTTP status, ever. Failures are either per-request
`result` messages or a Close frame.

Because the agent-facing listener negotiates both `h2` (for gRPC) and
`http/1.1` on the same port, the agent's dialer builds its own `tls.Config`
with `NextProtos` of exactly `["http/1.1"]`. Reusing the gRPC transport
credentials' config advertises `h2` and breaks the dial. The dialer also
leaves `Proxy` explicitly nil, so this one dial is direct and is not
redirected by the agent process's environment.

## The envelope

Every message is a WebSocket TEXT frame carrying exactly one JSON object with
four routing members plus a kind-specific body.

| Member | Type | Meaning |
|---|---|---|
| `v` | integer | Exactly `1` on this subprotocol |
| `kind` | string | `request`, `data`, `result` or `cancel` |
| `origin` | string | `service` or `agent`: which side's identifier namespace `id` belongs to |
| `id` | string | The request identifier within that namespace |

| `kind` | Direction | Body |
|---|---|---|
| `request` | originator → responder | `method` (string), `params` (object), `trace` (object, optional) |
| `data` | responder → originator | `payload` (object) |
| `result` | responder → originator | `status.reason` (string), `status.message` (optional). Terminal |
| `cancel` | originator → responder | `reason`, `message` (both optional) |

`origin` is a routing key and nothing else. It is never an authentication
claim and never a device or organization assertion — those come only from the
client certificate.

### Decoding is strict

`encoding/json` cannot enforce this contract, which is why
`internal/agentlink/protocol` carries its own reader. Three rules are outside
what the standard decoder offers at any setting: duplicate member names are
rejected rather than resolved last-wins, `null` is never read as an absent
member, and unpaired surrogate escapes are rejected rather than replaced with
U+FFFD. Silently rewriting a decoded string would corrupt content and, in an
`id` or `method`, would rewrite a routing key.

Also enforced: unknown members are rejected rather than ignored, types are
exact with no coercion, integers decode through a lossless decimal path rather
than a binary64 intermediate, every member has a finite length bound, the
decoder has a finite nesting depth, and a leading byte-order mark is rejected.

### Fault scope

Attributable faults fail one request; unattributable faults fail the link.
Nothing in between silently continues.

* **Link-scoped** — the message cannot be attributed at all: invalid JSON or
  not an object, invalid UTF-8, a BINARY frame, a missing or non-string
  routing member, a duplicate name or bad escape *in a routing member*, a `v`
  other than `1`, or a request naming a currently live identifier. The
  receiver closes with the applicable code (`1002`, `1007`, `1008`, `1009`)
  and every request on the link fails explicitly.
* **Request-scoped** — the message could be attributed: an unknown member, a
  wrong type, an unrecognised status reason. Exactly one request fails and the
  link continues. A malformed `request` is answered with one result so its
  originator is not left waiting; a malformed `data`, `result` or `cancel` is
  dropped and counted and never answered, which makes an error loop between
  two peers structurally impossible.

An unimplemented method is request-scoped in both directions: exactly one
`MethodNotSupported` result, no work started.

## Lifecycle

**One link, many logical requests.** The connection is not consumed by a
request. The link outlives any request; a request's end is never inferred from
the connection closing; and the connection closing fails every request still
running on it. A clean Close with code `1000` is never a request's success.

**Concurrency.** gorilla/websocket supports exactly one concurrent reader and
one concurrent writer, so each side runs one reader, one writer, one
supervisor and one worker per active request. The reader never waits on
application work — it hands off through bounded, non-blocking offers.

**Liveness** is native Ping/Pong driven by elapsed time on the connection,
never by application data, so an indefinitely quiet session stays alive. The
Pong-wait is armed from the *successful return* of the Ping's `WriteControl`,
never from a merely queued Ping: a failed control write is a socket fault, not
a missing Pong. No global read deadline is ever armed, because a timed-out
read corrupts the connection and would destroy every sibling request.

**Reconnect is recovery, never resumption.** On any connection end the agent
dials again with exponential backoff and full jitter, the first retry drawn
uniformly from 0–30s. The replacement link carries nothing over and starts
with no requests.

**Shutdown is ordered**, and the Close frame is sent last — closing first is
impossible, because the link is the only way to deliver a terminal result to
the requests still running on it:

1. Stop dialing, so a link that breaks from here is not replaced.
2. Refuse new starts locally, explicitly, per request.
3. Finish or fail each running request while the connection is still writable.
4. Send the Close frame (`1001`).

## Link registration

While a connection is open, the replica holding it registers the device under
`v1/agentlink/dispatch/{orgId}/{deviceName}` with a unique `linkId` and a 60s
TTL, refreshed every 20s. `linkId` stays internal and never appears in any
JSON message.

The newest link wins. Refresh and deletion compare the stored value, so only
the current holder may keep or remove its own registration and a closing link
can never delete its replacement's. Crucially, **the registration gates new
starts but not running work**: a superseded link is not closed, and the
requests already running on it continue to their own terminal results.

Registration absence proves little — an agent too old to dial and a supporting
agent that is merely offline both present the same way — and registration
presence proves only that the agent completed the handshake and speaks the
envelope, never that it implements any particular method.

## Where the code lives

| Path | Responsibility |
|---|---|
| `internal/agentlink/protocol/` | Wire contract: envelope types, strict decoder, encoders, limits. Pure, no I/O |
| `internal/agentlink/link/` | The live link: reader, writer, supervisor, attribution, shutdown. Shared by both ends |
| `internal/agentlink/registry.go` | Dispatch registration in the key-value store |
| `internal/api_server/agentserver/link.go` | The service-side route and upgrade |
| `internal/agent/device/link/` | The agent-side manager: dialing, reconnect, backoff |

Both ends run the same link machinery, so the contract is enforced once rather
than twice.

## Resource budgets

Every budget is a finite, positive constant compiled into the binary. None is
a configuration key and none is negotiated on the wire. They live in
`internal/agentlink/protocol/limits.go`.

One relationship is contract rather than sizing, and must hold in both
directions: **each endpoint's read limit must exceed the largest message its
peer may compose.** Both ends are built from the same constants, so a single
comparison covers it (1 MiB read limit against a 256 KiB composing bound).
