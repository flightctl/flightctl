package protocol

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// scope is the expected outcome of decoding a message: either it is accepted,
// or it fails one request, or it fails the link.
type scope int

const (
	accepted scope = iota
	requestScoped
	linkScoped
)

func TestDecodeRoutingMembers(t *testing.T) {
	testCases := []struct {
		name      string
		msg       string
		want      scope
		closeCode int
	}{
		{
			name: "When every routing member is present and valid it should be accepted",
			msg:  `{"v":1,"kind":"cancel","origin":"service","id":"abc"}`,
			want: accepted,
		},
		{
			name:      "When v is missing it should fail the link",
			msg:       `{"kind":"cancel","origin":"service","id":"abc"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When v is not 1 it should fail the link rather than be answered per request",
			msg:       `{"v":2,"kind":"cancel","origin":"service","id":"abc"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When v is written as 1.0 it should fail the link because integers carry no fraction",
			msg:       `{"v":1.0,"kind":"cancel","origin":"service","id":"abc"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When v is written as 1e0 it should fail the link because integers carry no exponent",
			msg:       `{"v":1e0,"kind":"cancel","origin":"service","id":"abc"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When v is a string it should fail the link because types are not coerced",
			msg:       `{"v":"1","kind":"cancel","origin":"service","id":"abc"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When kind is unknown it should fail the link",
			msg:       `{"v":1,"kind":"frobnicate","origin":"service","id":"abc"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When origin is unknown it should fail the link",
			msg:       `{"v":1,"kind":"cancel","origin":"somebody","id":"abc"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When id is not a string it should fail the link because the message is unattributable",
			msg:       `{"v":1,"kind":"cancel","origin":"service","id":42}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When id is null it should fail the link because null is not an absent member",
			msg:       `{"v":1,"kind":"cancel","origin":"service","id":null}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When id is empty it should fail the link",
			msg:       `{"v":1,"kind":"cancel","origin":"service","id":""}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When the top level is an array it should fail the link",
			msg:       `[{"v":1,"kind":"cancel","origin":"service","id":"abc"}]`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When the message is not JSON it should fail the link",
			msg:       `not json at all`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When the message carries a byte-order mark it should be rejected rather than skipped",
			msg:       "\xEF\xBB\xBF" + `{"v":1,"kind":"cancel","origin":"service","id":"abc"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When two messages are concatenated it should fail the link",
			msg:       `{"v":1,"kind":"cancel","origin":"service","id":"a"}{"v":1,"kind":"cancel","origin":"service","id":"b"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			_, err := Decode([]byte(tc.msg))
			assertScope(t, require, err, tc.want, tc.closeCode)
		})
	}
}

func TestDecodeStrictness(t *testing.T) {
	testCases := []struct {
		name      string
		msg       string
		want      scope
		closeCode int
	}{
		{
			name: "When an unknown top-level member is present it should be rejected rather than ignored",
			msg:  `{"v":1,"kind":"cancel","origin":"service","id":"abc","extra":true}`,
			want: requestScoped,
		},
		{
			name:      "When a routing member name is duplicated it should fail the link",
			msg:       `{"v":1,"kind":"cancel","origin":"service","id":"abc","id":"def"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name: "When a non-routing member name is duplicated it should fail only that request",
			msg:  `{"v":1,"kind":"cancel","origin":"service","id":"abc","reason":"a","reason":"b"}`,
			want: requestScoped,
		},
		{
			name:      "When an unpaired surrogate escape is in id it should fail the link rather than become U+FFFD",
			msg:       `{"v":1,"kind":"cancel","origin":"service","id":"\uD800"}`,
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name: "When an unpaired surrogate escape is outside the routing members it should fail only that request",
			msg:  `{"v":1,"kind":"cancel","origin":"service","id":"abc","message":"\uD800"}`,
			want: requestScoped,
		},
		{
			name: "When an undefined escape is used it should be rejected",
			msg:  `{"v":1,"kind":"cancel","origin":"service","id":"abc","message":"\q"}`,
			want: requestScoped,
		},
		{
			name: "When a valid surrogate pair is used it should be accepted",
			msg:  `{"v":1,"kind":"cancel","origin":"service","id":"abc","message":"😀"}`,
			want: accepted,
		},
		{
			name:      "When the message is not valid UTF-8 it should fail the link with 1007",
			msg:       "{\"v\":1,\"kind\":\"cancel\",\"origin\":\"service\",\"id\":\"\xff\xfe\"}",
			want:      linkScoped,
			closeCode: websocket.CloseInvalidFramePayloadData,
		},
		{
			name:      "When nesting exceeds the depth bound it should fail the link",
			msg:       deepMessage(MaxNestingDepth + 5),
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
		{
			name:      "When id exceeds its length bound it should fail the link before a reply is composed",
			msg:       fmt.Sprintf(`{"v":1,"kind":"cancel","origin":"service","id":%q}`, strings.Repeat("x", MaxIDLen+1)),
			want:      linkScoped,
			closeCode: websocket.CloseProtocolError,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			_, err := Decode([]byte(tc.msg))
			assertScope(t, require, err, tc.want, tc.closeCode)
		})
	}
}

func TestDecodeBodies(t *testing.T) {
	testCases := []struct {
		name   string
		msg    string
		want   scope
		verify func(*require.Assertions, *Envelope)
	}{
		{
			name: "When a request carries a method and empty params it should be accepted",
			msg:  `{"v":1,"kind":"request","origin":"service","id":"r1","method":"device.logs.get","params":{}}`,
			want: accepted,
			verify: func(r *require.Assertions, e *Envelope) {
				r.Equal("device.logs.get", e.Method)
				r.NotNil(e.Params)
			},
		},
		{
			name: "When a request omits params it should fail that request because absence is not an empty object",
			msg:  `{"v":1,"kind":"request","origin":"service","id":"r1","method":"device.logs.get"}`,
			want: requestScoped,
		},
		{
			name: "When a request carries trace context it should be accepted",
			msg:  `{"v":1,"kind":"request","origin":"service","id":"r1","method":"m","params":{},"trace":{"traceparent":"00-x-y-01"}}`,
			want: accepted,
			verify: func(r *require.Assertions, e *Envelope) {
				r.Equal("00-x-y-01", e.Trace["traceparent"])
			},
		},
		{
			name: "When trace carries an unknown member it should fail that request",
			msg:  `{"v":1,"kind":"request","origin":"service","id":"r1","method":"m","params":{},"trace":{"baggage":"x"}}`,
			want: requestScoped,
		},
		{
			name: "When a result carries a known reason it should be accepted",
			msg:  `{"v":1,"kind":"result","origin":"service","id":"r1","status":{"reason":"OK"}}`,
			want: accepted,
			verify: func(r *require.Assertions, e *Envelope) {
				r.Equal(ReasonOK, e.Status.Reason)
			},
		},
		{
			name: "When a result omits status.reason it should fail that request rather than be read as OK",
			msg:  `{"v":1,"kind":"result","origin":"service","id":"r1","status":{}}`,
			want: requestScoped,
		},
		{
			name: "When a result carries an unrecognised reason it should fail that request",
			msg:  `{"v":1,"kind":"result","origin":"service","id":"r1","status":{"reason":"Whatever"}}`,
			want: requestScoped,
		},
		{
			name: "When a result carries an unknown status member it should fail that request",
			msg:  `{"v":1,"kind":"result","origin":"service","id":"r1","status":{"reason":"OK","extra":1}}`,
			want: requestScoped,
		},
		{
			name: "When a data message carries a payload object it should be accepted",
			msg:  `{"v":1,"kind":"data","origin":"service","id":"r1","payload":{"content":"line"}}`,
			want: accepted,
		},
		{
			name: "When a data message omits payload it should fail that request",
			msg:  `{"v":1,"kind":"data","origin":"service","id":"r1"}`,
			want: requestScoped,
		},
		{
			name: "When a cancel omits its optional members it should be accepted",
			msg:  `{"v":1,"kind":"cancel","origin":"agent","id":"r1"}`,
			want: accepted,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			env, err := Decode([]byte(tc.msg))
			assertScope(t, require, err, tc.want, 0)
			if tc.want == accepted && tc.verify != nil {
				tc.verify(require, env)
			}
		})
	}
}

func TestDecodeRejectsOversizeMessage(t *testing.T) {
	require := require.New(t)

	// A message past the read limit is link-fatal, and the decoder enforces
	// the bound itself rather than relying on the library having done so.
	body := strings.Repeat("x", int(MaxMessageBytes))
	msg := fmt.Sprintf(`{"v":1,"kind":"cancel","origin":"service","id":"a","message":%q}`, body)

	_, err := Decode([]byte(msg))

	var lv *LinkViolation
	require.True(errors.As(err, &lv))
	require.Equal(websocket.CloseMessageTooBig, lv.CloseCode)
}

func TestSenderMayUse(t *testing.T) {
	testCases := []struct {
		name   string
		sender Origin
		kind   Kind
		origin Origin
		want   bool
	}{
		{
			name:   "When the named originator sends a request it should be permitted",
			sender: OriginService, kind: KindRequest, origin: OriginService, want: true,
		},
		{
			name:   "When the responder sends a request it should be refused",
			sender: OriginAgent, kind: KindRequest, origin: OriginService, want: false,
		},
		{
			name:   "When the responder sends data it should be permitted",
			sender: OriginAgent, kind: KindData, origin: OriginService, want: true,
		},
		{
			name:   "When the originator sends data it should be refused",
			sender: OriginService, kind: KindData, origin: OriginService, want: false,
		},
		{
			name:   "When the responder sends a result it should be permitted",
			sender: OriginAgent, kind: KindResult, origin: OriginService, want: true,
		},
		{
			name:   "When the named originator sends a cancel it should be permitted",
			sender: OriginService, kind: KindCancel, origin: OriginService, want: true,
		},
		{
			name:   "When the responder sends a cancel it should be refused",
			sender: OriginAgent, kind: KindCancel, origin: OriginService, want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.New(t).Equal(tc.want, SenderMayUse(tc.sender, tc.kind, tc.origin))
		})
	}
}

// TestReadLimitExceedsPeerComposingBound states the two inequalities the
// profile requires. Both ends are built from the same constants, so the
// single comparison covers both directions.
func TestReadLimitExceedsPeerComposingBound(t *testing.T) {
	require := require.New(t)
	require.Greater(MaxMessageBytes, int64(MaxComposedMessageBytes),
		"each endpoint's read limit must exceed the largest message its peer may compose")
	require.Positive(MaxComposedMessageBytes)
	require.Positive(MaxMessageBytes)
}

func assertScope(t *testing.T, require *require.Assertions, err error, want scope, closeCode int) {
	t.Helper()
	switch want {
	case accepted:
		require.NoError(err)
	case linkScoped:
		var lv *LinkViolation
		require.True(errors.As(err, &lv), "expected a link violation, got %v", err)
		if closeCode != 0 {
			require.Equal(closeCode, lv.CloseCode)
		}
	case requestScoped:
		var rv *RequestViolation
		require.True(errors.As(err, &rv), "expected a request violation, got %v", err)
		require.Equal(ReasonRetrievalFailed, rv.Reason)
		require.NotEmpty(rv.ID)
	}
}

func deepMessage(depth int) string {
	var sb strings.Builder
	sb.WriteString(`{"v":1,"kind":"request","origin":"service","id":"r1","method":"m","params":`)
	sb.WriteString(strings.Repeat(`{"a":`, depth))
	sb.WriteString("1")
	sb.WriteString(strings.Repeat("}", depth))
	sb.WriteString("}")
	return sb.String()
}
