package devicelogs_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agentlink/devicelogs"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/stretchr/testify/require"
)

// Params and payloads are reached through the real envelope decoder rather
// than built by hand, because the two halves of the contract only hold
// together if the shape this package validates is the shape that decoder
// produces.

func paramsValue(t *testing.T, raw string) *protocol.Value {
	t.Helper()
	msg := fmt.Sprintf(`{"v":1,"kind":"request","origin":"service","id":"req-1","method":%q,"params":%s}`,
		devicelogs.Method, raw)
	env, err := protocol.Decode([]byte(msg))
	require.NoError(t, err, "the envelope itself must decode; the test is about params")
	return env.Params
}

func payloadValue(t *testing.T, raw string) *protocol.Value {
	t.Helper()
	msg := fmt.Sprintf(`{"v":1,"kind":"data","origin":"service","id":"req-1","payload":%s}`, raw)
	env, err := protocol.Decode([]byte(msg))
	require.NoError(t, err)
	return env.Payload
}

func TestDecodeParamsAcceptsWellFormedRequests(t *testing.T) {
	sinceTime := time.Date(2026, 11, 17, 9, 14, 2, 481913000, time.UTC)
	untilTime := time.Date(2026, 11, 17, 9, 20, 0, 0, time.UTC)
	offset := int64(5)
	zeroOffset := int64(0)

	testCases := []struct {
		name string
		raw  string
		want devicelogs.Params
	}{
		{
			name: "When only the required members are present it should default everything else",
			raw:  `{"source":"journal","journal":{}}`,
			want: devicelogs.Params{Source: "journal", Journal: &devicelogs.JournalParams{}},
		},
		{
			name: "When every journal filter is present it should decode all of them",
			raw: `{"source":"journal","tailLines":100,"lineOffset":5,"journal":{` +
				`"units":["flightctl-agent.service","crio.*"],"priority":"info","boot":"previous",` +
				`"sinceTime":"2026-11-17T09:14:02.481913Z","untilTime":"2026-11-17T09:20:00.000000Z"}}`,
			want: devicelogs.Params{
				Source:     "journal",
				TailLines:  100,
				LineOffset: &offset,
				Journal: &devicelogs.JournalParams{
					Units:     []string{"flightctl-agent.service", "crio.*"},
					Priority:  "info",
					Boot:      "previous",
					SinceTime: &sinceTime,
					UntilTime: &untilTime,
				},
			},
		},
		{
			name: "When sinceSeconds is used it should decode without a sinceTime",
			raw:  `{"source":"journal","journal":{"sinceSeconds":600}}`,
			want: devicelogs.Params{Source: "journal", Journal: &devicelogs.JournalParams{SinceSeconds: 600}},
		},
		{
			name: "When lineOffset is explicitly zero it should be present rather than absent",
			raw:  `{"source":"journal","journal":{},"lineOffset":0}`,
			want: devicelogs.Params{Source: "journal", Journal: &devicelogs.JournalParams{}, LineOffset: &zeroOffset},
		},
		{
			name: "When follow is requested without an offset it should decode",
			raw:  `{"source":"journal","journal":{},"follow":true}`,
			want: devicelogs.Params{Source: "journal", Journal: &devicelogs.JournalParams{}, Follow: true},
		},
		{
			name: "When the source is a file it should decode the path",
			raw:  `{"source":"file","file":{"path":"/var/log/flightctl-agent.log"},"tailLines":2}`,
			want: devicelogs.Params{
				Source:    "file",
				TailLines: 2,
				File:      &devicelogs.FileParams{Path: "/var/log/flightctl-agent.log"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := devicelogs.DecodeParams(paramsValue(t, tc.raw))
			require.NoError(t, err)
			require.Equal(t, &tc.want, got)
		})
	}
}

func TestDecodeParamsRejectsIllFormedRequests(t *testing.T) {
	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "When source is absent it should be rejected",
			raw:  `{"journal":{}}`,
			want: `missing required member "source"`,
		},
		{
			name: "When source names something unknown it should be rejected",
			raw:  `{"source":"syslog","journal":{}}`,
			want: `not "journal" or "file"`,
		},
		{
			name: "When a byte-denominated limit is sent it should be rejected as an unknown member",
			raw:  `{"source":"journal","journal":{},"limitBytes":1024}`,
			want: `unknown member "limitBytes" in params`,
		},
		{
			name: "When the journal object carries an unknown member it should be rejected",
			raw:  `{"source":"journal","journal":{"maxBytes":1024}}`,
			want: `unknown member "maxBytes" in params.journal`,
		},
		{
			name: "When neither source object is present it should be rejected",
			raw:  `{"source":"journal"}`,
			want: `exactly one of "journal" and "file" must be present`,
		},
		{
			name: "When both source objects are present it should be rejected",
			raw:  `{"source":"journal","journal":{},"file":{"path":"/var/log/x"}}`,
			want: `exactly one of "journal" and "file" must be present`,
		},
		{
			name: "When the source object does not match source it should be rejected",
			raw:  `{"source":"file","journal":{}}`,
			want: `does not match the source object present`,
		},
		{
			name: "When sinceSeconds and sinceTime are both present it should be rejected rather than ranked",
			raw:  `{"source":"journal","journal":{"sinceSeconds":60,"sinceTime":"2026-11-17T09:14:02.481913Z"}}`,
			want: `mutually exclusive`,
		},
		{
			name: "When lineOffset accompanies follow it should be rejected",
			raw:  `{"source":"journal","journal":{},"follow":true,"lineOffset":0}`,
			want: `"lineOffset" in params may not be combined with "follow"`,
		},
		{
			name: "When tailLines is zero it should be rejected rather than read as absent",
			raw:  `{"source":"journal","journal":{},"tailLines":0}`,
			want: `outside 1..`,
		},
		{
			name: "When lineOffset is negative it should be rejected",
			raw:  `{"source":"journal","journal":{},"lineOffset":-1}`,
			want: `outside 0..`,
		},
		{
			name: "When tailLines is a string it should not be coerced",
			raw:  `{"source":"journal","journal":{},"tailLines":"100"}`,
			want: `is a string, not an integer`,
		},
		{
			name: "When tailLines is integral but written with an exponent it should be rejected",
			raw:  `{"source":"journal","journal":{},"tailLines":1e2}`,
			want: `has a fraction or exponent`,
		},
		{
			name: "When follow is the number one it should not be read as true",
			raw:  `{"source":"journal","journal":{},"follow":1}`,
			want: `is a number, not a boolean`,
		},
		{
			name: "When follow is explicitly null it should be rejected rather than treated as absent",
			raw:  `{"source":"journal","journal":{},"follow":null}`,
			want: `is a null, not a boolean`,
		},
		{
			name: "When units is a bare string it should be rejected rather than wrapped",
			raw:  `{"source":"journal","journal":{"units":"crio.service"}}`,
			want: `is a string, not an array`,
		},
		{
			name: "When a unit element is not a string it should fail the member",
			raw:  `{"source":"journal","journal":{"units":["crio.service",7]}}`,
			want: `element 1 of member "units"`,
		},
		{
			name: "When sinceTime carries a numeric offset it should be rejected rather than converted",
			raw:  `{"source":"journal","journal":{"sinceTime":"2026-11-17T09:14:02.481913+01:00"}}`,
			want: `not an RFC 3339 UTC instant`,
		},
		{
			name: "When sinceTime carries sub-microsecond digits it should be rejected rather than rounded",
			raw:  `{"source":"journal","journal":{"sinceTime":"2026-11-17T09:14:02.481913123Z"}}`,
			want: `not an RFC 3339 UTC instant`,
		},
		{
			name: "When sinceTime omits the fractional part it should be rejected",
			raw:  `{"source":"journal","journal":{"sinceTime":"2026-11-17T09:14:02Z"}}`,
			want: `not an RFC 3339 UTC instant`,
		},
		{
			name: "When the file path is empty it should be rejected",
			raw:  `{"source":"file","file":{"path":""}}`,
			want: `missing required member "path"`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := devicelogs.DecodeParams(paramsValue(t, tc.raw))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestEncodeParamsRoundTripsThroughTheDecoder(t *testing.T) {
	// The originator's half and the responder's half are one schema. A request
	// the encoder builds must be one the decoder accepts unchanged, or the two
	// ends disagree about a method neither of them versions.
	since := time.Date(2026, 11, 17, 9, 14, 2, 481913000, time.UTC)
	until := time.Date(2026, 11, 17, 9, 20, 0, 0, time.UTC)
	offset := int64(0)

	testCases := []struct {
		name   string
		params devicelogs.Params
	}{
		{
			name:   "When nothing optional is set",
			params: devicelogs.Params{Source: devicelogs.SourceJournal, Journal: &devicelogs.JournalParams{}},
		},
		{
			name: "When every journal filter is set",
			params: devicelogs.Params{
				Source:     devicelogs.SourceJournal,
				TailLines:  250,
				LineOffset: &offset,
				Journal: &devicelogs.JournalParams{
					Units:     []string{"flightctl-agent.service"},
					Priority:  "warning",
					Boot:      "current",
					SinceTime: &since,
					UntilTime: &until,
				},
			},
		},
		{
			name: "When a relative lower bound is used",
			params: devicelogs.Params{
				Source:  devicelogs.SourceJournal,
				Follow:  true,
				Journal: &devicelogs.JournalParams{SinceSeconds: 900},
			},
		},
		{
			name: "When the source is a file",
			params: devicelogs.Params{
				Source:    devicelogs.SourceFile,
				TailLines: 2,
				File:      &devicelogs.FileParams{Path: "/var/log/flightctl-agent.log"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := devicelogs.EncodeParams(&tc.params)
			require.NoError(t, err)

			got, err := devicelogs.DecodeParams(paramsValue(t, string(raw)))
			require.NoError(t, err)
			require.Equal(t, &tc.params, got)
		})
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	testCases := []struct {
		name    string
		payload devicelogs.Payload
	}{
		{
			name:    "When it carries ordinary content",
			payload: devicelogs.Payload{Content: "Nov 17 09:14:02 dev-01 agent[912]: syncing spec v42\n"},
		},
		{
			name:    "When it is a flag-only message its empty content is still content",
			payload: devicelogs.Payload{Content: "", BacklogComplete: true},
		},
		{
			name:    "When every flag is set",
			payload: devicelogs.Payload{Content: "x", BacklogComplete: true, Truncated: true, Rotated: true},
		},
		{
			name:    "When the content needs escaping",
			payload: devicelogs.Payload{Content: "tab\there \"quoted\" \x1b[0m \\ and \u0001\n"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := devicelogs.EncodePayload(tc.payload)
			require.NoError(t, err)

			got, err := devicelogs.DecodePayload(payloadValue(t, string(raw)))
			require.NoError(t, err)
			require.Equal(t, &tc.payload, got)
		})
	}
}

func TestDecodePayloadRejectsIllFormedMessages(t *testing.T) {
	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "When content is absent it should be rejected rather than defaulted to empty",
			raw:  `{"backlogComplete":true}`,
			want: `missing required member "content"`,
		},
		{
			name: "When content is explicitly null it should be rejected",
			raw:  `{"content":null}`,
			want: `is a null, not a string`,
		},
		{
			name: "When an unknown flag is present it should be rejected rather than ignored",
			raw:  `{"content":"x","compressed":true}`,
			want: `unknown member "compressed" in payload`,
		},
		{
			name: "When a flag is a string it should not be coerced",
			raw:  `{"content":"x","truncated":"true"}`,
			want: `is a string, not a boolean`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := devicelogs.DecodePayload(payloadValue(t, tc.raw))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestFormatTimestampTruncatesBelowMicroseconds(t *testing.T) {
	// The originator has already chosen its bound, so a sub-microsecond digit
	// is dropped rather than refused -- but it is dropped here, in the one
	// place that renders an instant, and never by the decoder on the far end.
	got := devicelogs.FormatTimestamp(time.Date(2026, 11, 17, 9, 14, 2, 481913999, time.UTC))
	require.Equal(t, "2026-11-17T09:14:02.481913Z", got)
}
