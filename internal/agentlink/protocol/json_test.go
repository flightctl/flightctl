package protocol

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseInt64Lexeme(t *testing.T) {
	testCases := []struct {
		name    string
		lexeme  string
		want    int64
		wantErr bool
	}{
		{name: "When the lexeme is zero it should decode", lexeme: "0", want: 0},
		{name: "When the lexeme is positive it should decode", lexeme: "1234", want: 1234},
		{name: "When the lexeme is negative it should decode", lexeme: "-7", want: -7},
		{
			name:   "When the lexeme is above 2^53 it should decode losslessly rather than through binary64",
			lexeme: "9007199254740993", want: 9007199254740993,
		},
		{
			name:   "When the lexeme is MaxInt64 it should decode exactly",
			lexeme: "9223372036854775807", want: math.MaxInt64,
		},
		{
			name:   "When the lexeme is MinInt64 it should decode exactly",
			lexeme: "-9223372036854775808", want: math.MinInt64,
		},
		{name: "When the lexeme overflows int64 it should be rejected rather than clamped", lexeme: "9223372036854775808", wantErr: true},
		{name: "When the lexeme has a fraction it should be rejected even though integral", lexeme: "1.0", wantErr: true},
		{name: "When the lexeme has an exponent it should be rejected even though integral", lexeme: "1e0", wantErr: true},
		{name: "When the lexeme exceeds the digit bound it should be rejected", lexeme: strings.Repeat("9", MaxIntegerDigits+1), wantErr: true},
		{name: "When the lexeme is empty it should be rejected", lexeme: "", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			got, err := parseInt64Lexeme(tc.lexeme)
			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
			require.Equal(tc.want, got)
		})
	}
}

// TestParseJSONPreservesSurrogateFaultPath covers the property the standard
// decoder cannot provide: an unpaired surrogate is reported, with its
// location, rather than silently rewritten to U+FFFD.
func TestParseJSONPreservesSurrogateFaultPath(t *testing.T) {
	require := require.New(t)

	_, faults, err := parseJSON([]byte(`{"outer":{"inner":"\uD800"}}`))

	require.NoError(err)
	require.Len(faults, 1)
	require.Equal("outer.inner", faults[0].path)
}

func TestParseJSONRecordsDuplicateMemberPath(t *testing.T) {
	require := require.New(t)

	_, faults, err := parseJSON([]byte(`{"a":{"b":1,"b":2}}`))

	require.NoError(err)
	require.Len(faults, 1)
	require.Equal("a.b", faults[0].path)
}

func TestEncodeRoundTrip(t *testing.T) {
	testCases := []struct {
		name   string
		encode func() ([]byte, error)
		verify func(*require.Assertions, *Envelope)
	}{
		{
			name: "When a request is encoded it should decode with its method and params",
			encode: func() ([]byte, error) {
				return EncodeRequest(OriginService, "r1", "device.logs.get", json.RawMessage(`{"units":["a"]}`), nil)
			},
			verify: func(r *require.Assertions, e *Envelope) {
				r.Equal(KindRequest, e.Kind)
				r.Equal(OriginService, e.Origin)
				r.Equal("device.logs.get", e.Method)
			},
		},
		{
			name: "When a request is encoded with no params it should carry an empty object rather than omit it",
			encode: func() ([]byte, error) {
				return EncodeRequest(OriginService, "r1", "m", nil, nil)
			},
			verify: func(r *require.Assertions, e *Envelope) {
				r.NotNil(e.Params)
				r.Equal(ValueObject, e.Params.Kind)
			},
		},
		{
			name: "When a result is encoded it should decode with its reason",
			encode: func() ([]byte, error) {
				return EncodeResult(OriginService, "r1", ReasonMethodNotSupported, "method \"x\" is not implemented")
			},
			verify: func(r *require.Assertions, e *Envelope) {
				r.Equal(ReasonMethodNotSupported, e.Status.Reason)
				r.Contains(e.Status.Message, "not implemented")
			},
		},
		{
			name: "When a cancel is encoded it should decode",
			encode: func() ([]byte, error) {
				return EncodeCancel(OriginService, "r1", ReasonCancelled, "caller disconnected")
			},
			verify: func(r *require.Assertions, e *Envelope) {
				r.Equal(KindCancel, e.Kind)
				r.Equal("caller disconnected", e.Cancel.Message)
			},
		},
		{
			name: "When data is encoded it should decode with its payload",
			encode: func() ([]byte, error) {
				return EncodeData(OriginService, "r1", json.RawMessage(`{"content":"hello"}`))
			},
			verify: func(r *require.Assertions, e *Envelope) {
				r.Equal(KindData, e.Kind)
				r.NotNil(e.Payload)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			msg, err := tc.encode()
			require.NoError(err)

			env, err := Decode(msg)
			require.NoError(err)
			require.Equal(Version, env.V)
			require.Equal("r1", env.ID)
			tc.verify(require, env)
		})
	}
}

// TestEncodeResultTruncatesDiagnostic shows that a responder never echoes
// unbounded input: an over-long diagnostic is cut to its bound before the
// message is built, so an oversized request cannot force an oversized reply.
func TestEncodeResultTruncatesDiagnostic(t *testing.T) {
	require := require.New(t)

	msg, err := EncodeResult(OriginAgent, "r1", ReasonRetrievalFailed, strings.Repeat("x", MaxStatusMessageLen*4))
	require.NoError(err)

	env, err := Decode(msg)
	require.NoError(err)
	require.LessOrEqual(len(env.Status.Message), MaxStatusMessageLen)
}

func TestEncodeResultRejectsUnknownReason(t *testing.T) {
	require := require.New(t)

	_, err := EncodeResult(OriginAgent, "r1", "SomethingInvented", "")

	require.Error(err)
}

func TestCloseTextIsBoundedToControlFrameCapacity(t *testing.T) {
	require := require.New(t)

	// A close frame carries at most 125 payload bytes, two of which are the
	// status code.
	require.LessOrEqual(len(CloseText(strings.Repeat("x", 500))), 123)
	require.LessOrEqual(len(CloseText(strings.Repeat("😀", 100))), 123)
}

func TestValidateComposedRejectsOversize(t *testing.T) {
	require := require.New(t)

	err := ValidateComposed([]byte(strings.Repeat("x", MaxComposedMessageBytes+1)))

	require.Error(err)
	require.Contains(err.Error(), fmt.Sprintf("%d", MaxComposedMessageBytes))
}
