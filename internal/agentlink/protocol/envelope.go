package protocol

import (
	"strings"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// Envelope is a decoded message. Every message in either direction carries
// the same four routing members plus a kind-specific body.
type Envelope struct {
	V      int64
	Kind   Kind
	Origin Origin
	ID     string

	// Request
	Method string
	Params *Value
	Trace  map[string]string

	// Data
	Payload *Value

	// Result
	Status *Status

	// Cancel
	Cancel *CancelBody
}

// Status is a result's terminal outcome for one request.
type Status struct {
	Reason  string
	Message string
}

// CancelBody carries a cancel's optional diagnostics.
type CancelBody struct {
	Reason  string
	Message string
}

// routingMembers are the four members that must be readable before a message
// can be attributed at all. A strictness fault in one of them makes the
// message unattributable and escalates the fault to link scope.
var routingMembers = map[string]bool{"v": true, "kind": true, "origin": true, "id": true}

// allowedMembers lists the members each kind defines. Unknown members are
// rejected rather than ignored: that is what keeps a future extension from
// being silently dropped by an older peer, and it is why an incompatible
// envelope change costs a new subprotocol token rather than a new field.
var allowedMembers = map[Kind]map[string]bool{
	KindRequest: {"v": true, "kind": true, "origin": true, "id": true, "method": true, "params": true, "trace": true},
	KindData:    {"v": true, "kind": true, "origin": true, "id": true, "payload": true},
	KindResult:  {"v": true, "kind": true, "origin": true, "id": true, "status": true},
	KindCancel:  {"v": true, "kind": true, "origin": true, "id": true, "reason": true, "message": true},
}

var allowedStatusMembers = map[string]bool{"reason": true, "message": true}

// utf8BOM is rejected rather than skipped. RFC 8259 requires a sender not to
// emit one and permits a receiver to ignore one; this profile takes the
// stricter option.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Decode parses and validates one reassembled TEXT message.
//
// The order is fixed and is itself part of the contract. The message is
// size-checked and UTF-8-validated before the JSON decode; the routing
// members are read before anything else, because until they are known the
// message cannot be attributed and every fault is necessarily link-scoped;
// and for a request the method is resolved before params is inspected, so an
// unimplemented method can be refused without validating a shape this
// endpoint does not know.
//
// The returned error is always either *LinkViolation or *RequestViolation.
func Decode(msg []byte) (*Envelope, error) {
	if int64(len(msg)) > MaxMessageBytes {
		return nil, linkFault(websocket.CloseMessageTooBig, "message too large",
			"message of %d bytes exceeds the %d byte limit", len(msg), MaxMessageBytes)
	}
	if !utf8.Valid(msg) {
		return nil, linkFault(websocket.CloseInvalidFramePayloadData, "invalid utf-8",
			"message is not valid UTF-8")
	}
	if len(msg) >= len(utf8BOM) && string(msg[:len(utf8BOM)]) == string(utf8BOM) {
		return nil, protocolFault("message begins with a byte-order mark")
	}

	root, faults, err := parseJSON(msg)
	if err != nil {
		// Not JSON at all: nothing to attribute.
		return nil, protocolFault("message is not valid JSON: %v", err)
	}
	if root.Kind != ValueObject {
		return nil, protocolFault("message is a JSON %s, not an object", root.Kind)
	}

	env, err := decodeRouting(root)
	if err != nil {
		return nil, err
	}

	// Now that the message is attributable, any fault recorded during the
	// parse can be scoped: one landing in a routing member leaves the message
	// unattributable after all, and fails the link.
	for _, f := range faults {
		if routingMembers[f.path] {
			return nil, protocolFault("%s in routing member %q", f.msg, f.path)
		}
	}
	if len(faults) > 0 {
		return nil, requestFault(env, "%s at %q", faults[0].msg, faults[0].path)
	}

	allowed := allowedMembers[env.Kind]
	for _, k := range root.Keys {
		if !allowed[k] {
			return nil, requestFault(env, "unknown member %q for kind %q", k, env.Kind)
		}
	}

	switch env.Kind {
	case KindRequest:
		return env, decodeRequest(root, env)
	case KindData:
		return env, decodeData(root, env)
	case KindResult:
		return env, decodeResult(root, env)
	case KindCancel:
		return env, decodeCancel(root, env)
	default:
		// decodeRouting already rejected any other kind.
		return nil, protocolFault("unhandled kind %q", env.Kind)
	}
}

// decodeRouting reads v, kind, origin and id. Every failure here is
// link-scoped: a message missing or mistyping one of these cannot be
// attributed, so it can be neither answered nor dropped against a request.
func decodeRouting(root *Value) (*Envelope, error) {
	env := &Envelope{}

	vm, ok := root.Member("v")
	if !ok {
		return nil, protocolFault("missing required member %q", "v")
	}
	if vm.Kind != ValueNumber {
		return nil, protocolFault("member %q is a %s, not an integer", "v", vm.Kind)
	}
	v, err := parseInt64Lexeme(vm.Num)
	if err != nil {
		return nil, protocolFault("member %q: %v", "v", err)
	}
	if v != Version {
		// The accepted set on this link is {1}. The receiver has no agreed
		// way to interpret the rest of the object, including its id, so it
		// must not attribute it, reply to it or downgrade silently.
		return nil, protocolFault("envelope version %d is not %d", v, Version)
	}
	env.V = v

	kindStr, err := routingString(root, "kind")
	if err != nil {
		return nil, err
	}
	switch Kind(kindStr) {
	case KindRequest, KindData, KindResult, KindCancel:
		env.Kind = Kind(kindStr)
	default:
		return nil, protocolFault("unknown kind %q", truncate(kindStr, 32))
	}

	originStr, err := routingString(root, "origin")
	if err != nil {
		return nil, err
	}
	switch Origin(originStr) {
	case OriginService, OriginAgent:
		env.Origin = Origin(originStr)
	default:
		return nil, protocolFault("unknown origin %q", truncate(originStr, 32))
	}

	id, err := routingString(root, "id")
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, protocolFault("member %q is empty", "id")
	}
	if len(id) > MaxIDLen {
		// Rejected on the length bound before any reply is composed, so an
		// oversized id can never force an oversized response. Because the
		// over-long member is id, the message is unattributable.
		return nil, protocolFault("member %q exceeds %d bytes", "id", MaxIDLen)
	}
	env.ID = id

	return env, nil
}

func routingString(root *Value, name string) (string, error) {
	m, ok := root.Member(name)
	if !ok {
		return "", protocolFault("missing required member %q", name)
	}
	if m.Kind != ValueString {
		// null is not an absent member, and a non-string id is unattributable.
		return "", protocolFault("member %q is a %s, not a string", name, m.Kind)
	}
	return m.Str, nil
}

func decodeRequest(root *Value, env *Envelope) error {
	// Method is resolved before params is decoded. That order is what makes
	// MethodNotSupported reachable: under the strict unknown-member rule the
	// params of an unimplemented method is a shape this endpoint cannot
	// validate, so deciding params first would report every future method as
	// a failure.
	m, ok := root.Member("method")
	if !ok {
		return requestFault(env, "missing required member %q", "method")
	}
	if m.Kind != ValueString {
		return requestFault(env, "member %q is a %s, not a string", "method", m.Kind)
	}
	if m.Str == "" {
		return requestFault(env, "member %q is empty", "method")
	}
	if len(m.Str) > MaxMethodLen {
		return requestFault(env, "member %q exceeds %d bytes", "method", MaxMethodLen)
	}
	env.Method = m.Str

	p, ok := root.Member("params")
	if !ok {
		return requestFault(env, "missing required member %q", "params")
	}
	if p.Kind != ValueObject {
		return requestFault(env, "member %q is a %s, not an object", "params", p.Kind)
	}
	env.Params = p

	if t, ok := root.Member("trace"); ok {
		trace, err := decodeTrace(t, env)
		if err != nil {
			return err
		}
		env.Trace = trace
	}

	return nil
}

func decodeTrace(t *Value, env *Envelope) (map[string]string, error) {
	if t.Kind != ValueObject {
		return nil, requestFault(env, "member %q is a %s, not an object", "trace", t.Kind)
	}
	if len(t.Keys) > MaxTraceMembers {
		return nil, requestFault(env, "member %q has more than %d members", "trace", MaxTraceMembers)
	}
	trace := make(map[string]string, len(t.Keys))
	for _, k := range t.Keys {
		if k != "traceparent" && k != "tracestate" {
			return nil, requestFault(env, "unknown member %q in %q", truncate(k, 32), "trace")
		}
		m := t.Object[k]
		if m.Kind != ValueString {
			return nil, requestFault(env, "member %q in %q is a %s, not a string", k, "trace", m.Kind)
		}
		if len(m.Str) > MaxTraceMemberLen {
			return nil, requestFault(env, "member %q in %q exceeds %d bytes", k, "trace", MaxTraceMemberLen)
		}
		trace[k] = m.Str
	}
	return trace, nil
}

func decodeData(root *Value, env *Envelope) error {
	p, ok := root.Member("payload")
	if !ok {
		return requestFault(env, "missing required member %q", "payload")
	}
	if p.Kind != ValueObject {
		return requestFault(env, "member %q is a %s, not an object", "payload", p.Kind)
	}
	env.Payload = p
	return nil
}

func decodeResult(root *Value, env *Envelope) error {
	s, ok := root.Member("status")
	if !ok {
		return requestFault(env, "missing required member %q", "status")
	}
	if s.Kind != ValueObject {
		return requestFault(env, "member %q is a %s, not an object", "status", s.Kind)
	}
	for _, k := range s.Keys {
		if !allowedStatusMembers[k] {
			return requestFault(env, "unknown member %q in %q", truncate(k, 32), "status")
		}
	}

	r, ok := s.Member("reason")
	if !ok {
		// JSON has no default for an absent member and this profile assigns
		// none: absence is a violation, never a silent OK.
		return requestFault(env, "missing required member %q in %q", "reason", "status")
	}
	if r.Kind != ValueString {
		return requestFault(env, "member %q in %q is a %s, not a string", "reason", "status", r.Kind)
	}
	if len(r.Str) > MaxReasonLen {
		return requestFault(env, "member %q in %q exceeds %d bytes", "reason", "status", MaxReasonLen)
	}
	if !IsValidReason(r.Str) {
		return requestFault(env, "unrecognised status reason %q", truncate(r.Str, MaxReasonLen))
	}

	status := &Status{Reason: r.Str}
	if m, ok := s.Member("message"); ok {
		if m.Kind != ValueString {
			return requestFault(env, "member %q in %q is a %s, not a string", "message", "status", m.Kind)
		}
		if len(m.Str) > MaxStatusMessageLen {
			return requestFault(env, "member %q in %q exceeds %d bytes", "message", "status", MaxStatusMessageLen)
		}
		status.Message = m.Str
	}
	env.Status = status
	return nil
}

func decodeCancel(root *Value, env *Envelope) error {
	cancel := &CancelBody{}
	if r, ok := root.Member("reason"); ok {
		if r.Kind != ValueString {
			return requestFault(env, "member %q is a %s, not a string", "reason", r.Kind)
		}
		if len(r.Str) > MaxReasonLen {
			return requestFault(env, "member %q exceeds %d bytes", "reason", MaxReasonLen)
		}
		cancel.Reason = r.Str
	}
	if m, ok := root.Member("message"); ok {
		if m.Kind != ValueString {
			return requestFault(env, "member %q is a %s, not a string", "message", m.Kind)
		}
		if len(m.Str) > MaxStatusMessageLen {
			return requestFault(env, "member %q exceeds %d bytes", "message", MaxStatusMessageLen)
		}
		cancel.Message = m.Str
	}
	env.Cancel = cancel
	return nil
}

// truncate bounds a string before it is placed in a diagnostic, so a
// responder never echoes unbounded input back to its peer. It cuts on a rune
// boundary so the result stays a sequence of Unicode scalar values.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// SenderMayUse reports whether an endpoint acting as `sender` is permitted to
// send `kind` for a request whose namespace is `origin`.
//
// Sender role is enforced, not assumed: a request or cancel arriving from the
// endpoint that is not the named originator, or a data or result arriving
// from the endpoint that is, is a request-scoped protocol violation --
// dropped and counted, nothing started and nothing answered, and never
// re-resolved as though it had come from the other side.
func SenderMayUse(sender Origin, kind Kind, origin Origin) bool {
	switch kind {
	case KindRequest, KindCancel:
		return sender == origin
	case KindData, KindResult:
		return sender == origin.Opposite()
	default:
		return false
	}
}

// ValidateComposed checks a message this endpoint built against the composing
// bound, so an oversized message is caught before the write rather than by
// the peer's link-fatal read limit.
func ValidateComposed(msg []byte) error {
	if len(msg) > MaxComposedMessageBytes {
		return linkFault(websocket.CloseInternalServerErr, "internal error",
			"composed message of %d bytes exceeds the %d byte composing bound",
			len(msg), MaxComposedMessageBytes)
	}
	if !utf8.Valid(msg) {
		return linkFault(websocket.CloseInternalServerErr, "internal error",
			"composed message is not valid UTF-8")
	}
	return nil
}

// CloseText bounds a close-frame reason to the 123 bytes a close frame can
// carry beyond its two status bytes.
func CloseText(s string) string {
	const maxCloseText = 123
	return truncate(strings.TrimSpace(s), maxCloseText)
}
