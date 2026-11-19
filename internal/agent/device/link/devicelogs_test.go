package link

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentclient "github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/logs"
	"github.com/flightctl/flightctl/internal/agentlink/devicelogs"
	agentlink "github.com/flightctl/flightctl/internal/agentlink/link"
	"github.com/flightctl/flightctl/internal/agentlink/protocol"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const testRequestID = "01JD4M2Q7F0000000000000001"

// recordingResponder captures what a handler emitted for its one request.
//
// Each data message is composed and decoded through the real encoders, not
// inspected as a Go value, because two of the properties under test are
// properties of the wire form: that a chunk fits inside the composing bound
// once escaped, and that the strict decoder accepts what the agent builds.
type recordingResponder struct {
	payloads []devicelogs.Payload
	// wireBytes is the serialized size of each data message.
	wireBytes []int

	resultSent bool
	reason     string
	message    string
}

func (r *recordingResponder) Data(payload json.RawMessage) error {
	msg, err := protocol.EncodeData(protocol.OriginService, testRequestID, payload)
	if err != nil {
		return err
	}
	env, err := protocol.Decode(msg)
	if err != nil {
		return err
	}
	decoded, err := devicelogs.DecodePayload(env.Payload)
	if err != nil {
		return err
	}
	r.payloads = append(r.payloads, *decoded)
	r.wireBytes = append(r.wireBytes, len(msg))
	return nil
}

func (r *recordingResponder) Result(reason, message string) error {
	if r.resultSent {
		return nil
	}
	if _, err := protocol.EncodeResult(protocol.OriginService, testRequestID, reason, message); err != nil {
		return err
	}
	r.resultSent, r.reason, r.message = true, reason, message
	return nil
}

// content is everything the handler sent, concatenated. Message boundaries are
// invisible to a reader, so this is the only thing a caller sees.
func (r *recordingResponder) content() string {
	var b strings.Builder
	for _, p := range r.payloads {
		b.WriteString(p.Content)
	}
	return b.String()
}

// journalReturning builds a Journal whose single journalctl invocation answers
// with the given stdout, stderr and exit code.
func journalReturning(t *testing.T, stdout, stderr string, exitCode int) *logs.Journal {
	t.Helper()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithBoundedOutputFromDir(gomock.Any(), "", gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		Return(stdout, stderr, exitCode)

	return logs.NewJournal(log.NewPrefixLogger("test"),
		agentclient.NewJournalctl(mockExec, v1beta1.Username("")))
}

// runHandler decodes raw params through the envelope and runs the handler.
func runHandler(t *testing.T, ctx context.Context, journal *logs.Journal, rawParams string) *recordingResponder {
	t.Helper()

	msg := fmt.Sprintf(`{"v":1,"kind":"request","origin":"service","id":%q,"method":%q,"params":%s}`,
		testRequestID, devicelogs.Method, rawParams)
	env, err := protocol.Decode([]byte(msg))
	require.NoError(t, err)

	handlers := Handlers(journal, log.NewPrefixLogger("test"))
	handler, ok := handlers[devicelogs.Method]
	require.True(t, ok, "the agent must implement %s", devicelogs.Method)

	out := &recordingResponder{}
	handler(ctx, &agentlink.Request{
		Origin: env.Origin,
		ID:     env.ID,
		Method: env.Method,
		Params: env.Params,
	}, out)
	require.True(t, out.resultSent, "a handler must always fix an outcome")
	return out
}

func TestHandlersImplementsOnlyTheLogMethod(t *testing.T) {
	// The allowlist is closed. Anything outside it is refused by the link with
	// one MethodNotSupported and never reaches a handler, so the test of that
	// property is that the map has nothing else in it.
	handlers := Handlers(journalReturning(t, "", "", 0), log.NewPrefixLogger("test"))
	require.Len(t, handlers, 1)
	require.Contains(t, handlers, devicelogs.Method)
}

func TestDeviceLogsHandlerReturnsTheRequestedLines(t *testing.T) {
	stdout := "line-1\nline-2\nline-3\n"

	out := runHandler(t, context.Background(), journalReturning(t, stdout, "", 0),
		`{"source":"journal","journal":{"units":["flightctl-agent.service"],"priority":"info"}}`)

	require.Equal(t, protocol.ReasonOK, out.reason)
	require.Equal(t, stdout, out.content())
	require.Len(t, out.payloads, 1)
	require.False(t, out.payloads[0].Truncated)
	require.False(t, out.payloads[0].BacklogComplete)
}

func TestDeviceLogsHandlerAppliesTheRenderedLineWindow(t *testing.T) {
	// The window is denominated in rendered lines, and an entry that renders
	// to several lines is several lines here. This asserts the handler hands
	// the window through rather than reinterpreting it.
	stdout := "a-1\na-2\nb-1\nc-1\nc-2\nc-3\n"

	testCases := []struct {
		name   string
		params string
		want   string
	}{
		{
			name:   "When tailLines is given it should count back from the end",
			params: `{"source":"journal","journal":{},"tailLines":2}`,
			want:   "c-2\nc-3\n",
		},
		{
			name:   "When tailLines and lineOffset are given it should drop the trailing offset first",
			params: `{"source":"journal","journal":{},"tailLines":2,"lineOffset":1}`,
			want:   "c-1\nc-2\n",
		},
		{
			name:   "When only lineOffset is given it should skip from the head",
			params: `{"source":"journal","journal":{},"lineOffset":4}`,
			want:   "c-2\nc-3\n",
		},
		{
			name:   "When the offset is past the end it should be an empty success",
			params: `{"source":"journal","journal":{},"lineOffset":99}`,
			want:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out := runHandler(t, context.Background(), journalReturning(t, stdout, "", 0), tc.params)
			require.Equal(t, protocol.ReasonOK, out.reason)
			require.Equal(t, tc.want, out.content())
		})
	}
}

func TestDeviceLogsHandlerSendsNoDataForAnEmptySelection(t *testing.T) {
	out := runHandler(t, context.Background(), journalReturning(t, "", "", 0),
		`{"source":"journal","journal":{"units":["nothing-matches.service"]}}`)

	require.Equal(t, protocol.ReasonOK, out.reason)
	require.Empty(t, out.payloads, "an empty selection is an empty success, not an empty content message")
}

func TestDeviceLogsHandlerChunksLargeSnapshots(t *testing.T) {
	// Enough lines to need several chunks, each line distinguishable so the
	// reassembled text can be compared exactly.
	var source strings.Builder
	const lines = 4000
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&source, "%05d %s\n", i, strings.Repeat("x", 60))
	}

	out := runHandler(t, context.Background(), journalReturning(t, source.String(), "", 0),
		`{"source":"journal","journal":{}}`)

	require.Equal(t, protocol.ReasonOK, out.reason)
	require.Greater(t, len(out.payloads), 1, "a snapshot this size must span several messages")
	require.Equal(t, source.String(), out.content(), "message boundaries must not change the content")

	for i, p := range out.payloads {
		require.LessOrEqual(t, len(p.Content), ContentChunkBytes, "chunk %d exceeds the chunk bound", i)
		require.True(t, strings.HasSuffix(p.Content, "\n"),
			"chunk %d should end on a newline boundary when whole lines fit", i)
		require.LessOrEqual(t, out.wireBytes[i], protocol.MaxComposedMessageBytes,
			"chunk %d does not fit the composing bound once escaped", i)
	}
}

func TestDeviceLogsHandlerSplitsAnOverlongLineOnARuneBoundary(t *testing.T) {
	// A three-byte rune repeated past the chunk bound, so a naive cut at the
	// bound lands inside a code point.
	const snowman = "☃"
	line := strings.Repeat(snowman, ContentChunkBytes) // 3x the bound in bytes
	source := line + "\n"

	out := runHandler(t, context.Background(), journalReturning(t, source, "", 0),
		`{"source":"journal","journal":{}}`)

	require.Equal(t, protocol.ReasonOK, out.reason)
	require.Greater(t, len(out.payloads), 1)
	require.Equal(t, source, out.content())

	for i, p := range out.payloads {
		require.True(t, utf8.ValidString(p.Content), "chunk %d was split mid-code-point", i)
		require.LessOrEqual(t, len(p.Content), ContentChunkBytes)
		require.LessOrEqual(t, out.wireBytes[i], protocol.MaxComposedMessageBytes)
	}
	// Everything but the last message is a continuation of the same logical
	// line, and says so by not ending with a newline.
	for i := 0; i < len(out.payloads)-1; i++ {
		require.False(t, strings.HasSuffix(out.payloads[i].Content, "\n"),
			"chunk %d is a continuation and must not end with a newline", i)
	}
	require.True(t, strings.HasSuffix(out.payloads[len(out.payloads)-1].Content, "\n"))
}

func TestDeviceLogsHandlerChunkBoundHoldsAgainstWorstCaseEscaping(t *testing.T) {
	// The chunk bound is sized against the escaped form, so the case that
	// justifies the number is the one where every byte escapes to six: a run
	// of control characters written as \u00XX. If this fits, nothing fits
	// worse.
	source := strings.Repeat("\x01", ContentChunkBytes*3) + "\n"

	out := runHandler(t, context.Background(), journalReturning(t, source, "", 0),
		`{"source":"journal","journal":{}}`)

	require.Equal(t, protocol.ReasonOK, out.reason)
	require.Equal(t, source, out.content())
	require.Greater(t, len(out.payloads), 1)
	for i, size := range out.wireBytes {
		require.LessOrEqual(t, size, protocol.MaxComposedMessageBytes,
			"chunk %d of worst-case content does not fit the composing bound", i)
	}
}

func TestDeviceLogsHandlerRefusesParametersItCannotServe(t *testing.T) {
	testCases := []struct {
		name   string
		params string
		want   string
	}{
		{
			name:   "When the parameters are malformed it should fail the request",
			params: `{"source":"journal","journal":{},"limitBytes":100}`,
			want:   `unknown member "limitBytes"`,
		},
		{
			name:   "When a unit pattern is outside the charset it should fail the request",
			params: `{"source":"journal","journal":{"units":["crio[0-9].service"]}}`,
			want:   "unsupported character",
		},
		{
			name:   "When the priority is unknown it should fail the request",
			params: `{"source":"journal","journal":{"priority":"verbose"}}`,
			want:   "unknown priority",
		},
		{
			name:   "When follow is requested it should fail the request rather than claim the method is absent",
			params: `{"source":"journal","journal":{},"follow":true}`,
			want:   "follow is not implemented",
		},
		{
			name:   "When the file source is requested it should fail the request rather than claim the method is absent",
			params: `{"source":"file","file":{"path":"/var/log/flightctl-agent.log"}}`,
			want:   "is not implemented",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out := runHandler(t, context.Background(), journalReturning(t, "ignored\n", "", 0), tc.params)
			// RetrievalFailed and not MethodNotSupported: the agent does
			// implement this method, and saying otherwise would be read as a
			// capability verdict about the whole method.
			require.Equal(t, protocol.ReasonRetrievalFailed, out.reason)
			require.Contains(t, out.message, tc.want)
			require.Empty(t, out.payloads, "a refused request must start no source work")
		})
	}
}

func TestDeviceLogsHandlerMapsDeviceFailuresOntoStatusReasons(t *testing.T) {
	testCases := []struct {
		name     string
		stderr   string
		exitCode int
		want     string
	}{
		{
			name:     "When the requested boot is absent it should report BootUnavailable",
			stderr:   "Failed to look up boot -1: No such boot ID in journal",
			exitCode: 1,
			want:     protocol.ReasonBootUnavailable,
		},
		{
			name:     "When journalctl is missing it should report SourceNotFound",
			stderr:   "exec: \"journalctl\": executable file not found in $PATH",
			exitCode: 127,
			want:     protocol.ReasonSourceNotFound,
		},
		{
			name:     "When journalctl fails for an unclassified reason it should report RetrievalFailed",
			stderr:   "Permission denied while opening the journal",
			exitCode: 1,
			want:     protocol.ReasonRetrievalFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out := runHandler(t, context.Background(),
				journalReturning(t, "", tc.stderr, tc.exitCode),
				`{"source":"journal","journal":{"boot":"previous"}}`)

			require.Equal(t, tc.want, out.reason)
			require.Empty(t, out.payloads, "a pre-content failure must arrive with no data in front of it")
		})
	}
}

func TestDeviceLogsHandlerReportsAnAgentBudgetAsDeviceTimeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	// journalctl that honours the scan deadline rather than answering.
	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithBoundedOutputFromDir(gomock.Any(), "", gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(ctx context.Context, _, _ string, _ []string, _ int, _ ...string) (string, string, int) {
			<-ctx.Done()
			return "", "context deadline exceeded", 1
		})

	journal := logs.NewJournal(log.NewPrefixLogger("test"),
		agentclient.NewJournalctl(mockExec, v1beta1.Username("")),
		logs.WithScanBudget(50*time.Millisecond))

	out := runHandler(t, context.Background(), journal, `{"source":"journal","journal":{}}`)

	// The agent's own budget, not the caller's: the caller's context is live.
	require.Equal(t, protocol.ReasonDeviceTimeout, out.reason)
	require.Empty(t, out.payloads)
}

func TestDeviceLogsHandlerReportsCancellationAsCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	ctx, cancel := context.WithCancel(context.Background())

	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithBoundedOutputFromDir(gomock.Any(), "", gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(execCtx context.Context, _, _ string, _ []string, _ int, _ ...string) (string, string, int) {
			cancel()
			<-execCtx.Done()
			return "", "context canceled", 1
		})

	journal := logs.NewJournal(log.NewPrefixLogger("test"),
		agentclient.NewJournalctl(mockExec, v1beta1.Username("")))

	out := runHandler(t, ctx, journal, `{"source":"journal","journal":{}}`)

	// Cancellation means the originator already fixed this outcome, so
	// whatever the source reported on its way out is a consequence.
	require.Equal(t, protocol.ReasonCancelled, out.reason)
	require.Empty(t, out.payloads)
}

func TestDeviceLogsHandlerReportsAWorkingBufferOverrunRatherThanTruncating(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	const bufferLimit = 4096
	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithBoundedOutputFromDir(gomock.Any(), "", gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		Return(strings.Repeat("a", bufferLimit+1), "", 0)

	journal := logs.NewJournal(log.NewPrefixLogger("test"),
		agentclient.NewJournalctl(mockExec, v1beta1.Username("")),
		logs.WithMaxBufferBytes(bufferLimit))

	out := runHandler(t, context.Background(), journal, `{"source":"journal","journal":{}}`)

	require.Equal(t, protocol.ReasonResponseTooLarge, out.reason)
	require.Empty(t, out.payloads, "the bytes that fit are never returned as a success")
}
