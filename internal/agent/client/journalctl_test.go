package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	require.NoError(t, err)
	return parsed
}

func TestJournalctlArgumentConstruction(t *testing.T) {
	testCases := []struct {
		name         string
		options      []LogOptions
		expectedArgs []string
	}{
		{
			name:         "When no filter is given it should ask for the short format with no pager",
			options:      nil,
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short"},
		},
		{
			name:         "When several units are given it should repeat -u once per unit",
			options:      []LogOptions{WithLogUnit("flightctl-agent.service"), WithLogUnit("fctl-*.service")},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "-u", "flightctl-agent.service", "-u", "fctl-*.service"},
		},
		{
			name:         "When a priority is given it should pass its syslog level so more severe levels are included",
			options:      []LogOptions{WithLogPriority(JournalPriorityError)},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "-p", "3"},
		},
		{
			name:         "When the current boot is selected it should pass -b 0",
			options:      []LogOptions{WithLogBoot(JournalBootCurrent)},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "-b", "0"},
		},
		{
			name:         "When the previous boot is selected it should pass -b -1",
			options:      []LogOptions{WithLogBoot(JournalBootPrevious)},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "-b", "-1"},
		},
		{
			name:         "When a relative window is given it should pass it as a since expression",
			options:      []LogOptions{WithLogSinceSeconds(300)},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "--since", "300 seconds ago"},
		},
		{
			name:         "When an absolute lower bound is given it should normalize it to microsecond UTC",
			options:      []LogOptions{WithLogSinceTimestamp(mustTime(t, "2026-11-17T09:14:02.481913Z"))},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "--since", "2026-11-17 09:14:02.481913 UTC"},
		},
		{
			name:         "When an absolute upper bound is given it should normalize it to microsecond UTC",
			options:      []LogOptions{WithLogUntilTimestamp(mustTime(t, "2026-11-17T09:14:02.481913Z"))},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "--until", "2026-11-17 09:14:02.481913 UTC"},
		},
		{
			name:         "When a non-UTC offset is given it should convert the bound to UTC",
			options:      []LogOptions{WithLogSinceTimestamp(mustTime(t, "2026-11-17T09:14:02.481913+02:00"))},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "--since", "2026-11-17 07:14:02.481913 UTC"},
		},
		{
			name:         "When a sub-microsecond lower bound is given it should round up to the next tick",
			options:      []LogOptions{WithLogSinceTimestamp(mustTime(t, "2026-11-17T09:14:02.4819131Z"))},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "--since", "2026-11-17 09:14:02.481914 UTC"},
		},
		{
			name:         "When a sub-microsecond upper bound is given it should round down to the tick at or before it",
			options:      []LogOptions{WithLogUntilTimestamp(mustTime(t, "2026-11-17T09:14:02.4819139Z"))},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "--until", "2026-11-17 09:14:02.481913 UTC"},
		},
		{
			name:         "When a whole-second bound is given it should still write the fractional part rather than fall back to seconds",
			options:      []LogOptions{WithLogSinceTimestamp(mustTime(t, "2026-11-17T09:14:02Z"))},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "--since", "2026-11-17 09:14:02.000000 UTC"},
		},
		{
			name:         "When an entry budget is given it should pass it as -n",
			options:      []LogOptions{WithLogMaxEntries(2048)},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "-n", "2048"},
		},
		{
			name: "When every journal filter is combined it should pass each of them once",
			options: []LogOptions{
				WithLogUnit("flightctl-agent.service"),
				WithLogPriority(JournalPriorityWarning),
				WithLogBoot(JournalBootCurrent),
				WithLogSinceTimestamp(mustTime(t, "2026-11-17T09:00:00Z")),
				WithLogUntilTimestamp(mustTime(t, "2026-11-17T10:00:00Z")),
				WithLogMaxEntries(10),
			},
			expectedArgs: []string{
				"--no-pager", "--quiet", "-o", "short",
				"-u", "flightctl-agent.service",
				"-p", "4",
				"-b", "0",
				"--since", "2026-11-17 09:00:00.000000 UTC",
				"--until", "2026-11-17 10:00:00.000000 UTC",
				"-n", "10",
			},
		},
		{
			name:         "When a unit value contains shell metacharacters it should pass them as one literal argv element",
			options:      []LogOptions{WithLogUnit("a; touch /tmp/x")},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "-u", "a; touch /tmp/x"},
		},
		{
			name:         "When a unit value contains a command substitution it should pass it as one literal argv element",
			options:      []LogOptions{WithLogUnit("$(reboot)")},
			expectedArgs: []string{"--no-pager", "--quiet", "-o", "short", "-u", "$(reboot)"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			expected := make([]any, 0, len(tc.expectedArgs))
			for _, arg := range tc.expectedArgs {
				expected = append(expected, arg)
			}
			mockExec.EXPECT().
				ExecuteWithContext(gomock.Any(), journalctlCommand, expected...).
				Return("", "", 0)

			j := NewJournalctl(mockExec, v1beta1.Username(""))
			out, err := j.Output(context.Background(), tc.options...)
			require.NoError(err)
			require.Equal("", out)
		})
	}
}

func TestJournalctlOptionRejection(t *testing.T) {
	testCases := []struct {
		name   string
		option LogOptions
	}{
		{
			name:   "When the priority is not one this API defines it should refuse rather than drop the filter",
			option: WithLogPriority("critical-ish"),
		},
		{
			name:   "When the boot selector is not one this API defines it should refuse rather than drop the filter",
			option: WithLogBoot("next"),
		},
		{
			name:   "When sinceSeconds is below one it should refuse",
			option: WithLogSinceSeconds(0),
		},
		{
			name:   "When the entry budget is below one it should refuse",
			option: WithLogMaxEntries(0),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// No invocation is expected: an unexpressible filter must not
			// reach the device as a widened query.
			mockExec := executer.NewMockExecuter(ctrl)
			j := NewJournalctl(mockExec, v1beta1.Username(""))

			_, err := j.Output(context.Background(), tc.option)
			require.ErrorIs(err, errors.ErrLogInvalidParameters)
		})
	}
}

func TestValidateJournalUnit(t *testing.T) {
	testCases := []struct {
		name          string
		unit          string
		expectedError bool
	}{
		{name: "When the unit is a plain service name it should be accepted", unit: "fctl-alpha.service"},
		{name: "When the unit uses a star wildcard it should be accepted", unit: "fctl-*.service"},
		{name: "When the unit uses a question-mark wildcard it should be accepted", unit: "fctl-?.service"},
		{name: "When the unit is a templated instance it should be accepted", unit: "getty@tty1.service"},
		{name: "When the unit uses the remaining charset punctuation it should be accepted", unit: "a:b_c-d.e!f"},
		{name: "When the unit is a character class it should be rejected", unit: "fctl-[ab]*.service", expectedError: true},
		{name: "When the unit is empty it should be rejected", unit: "", expectedError: true},
		{name: "When the unit exceeds the length limit it should be rejected", unit: strings.Repeat("a", JournalMaxUnitLength+1), expectedError: true},
		{name: "When the unit is exactly at the length limit it should be accepted", unit: strings.Repeat("a", JournalMaxUnitLength)},
		{name: "When the unit contains a semicolon it should be rejected", unit: "a; touch /tmp/x", expectedError: true},
		{name: "When the unit contains a backtick it should be rejected", unit: "a`reboot`", expectedError: true},
		{name: "When the unit contains a command substitution it should be rejected", unit: "$(reboot)", expectedError: true},
		{name: "When the unit contains a pipe it should be rejected", unit: "a|b", expectedError: true},
		{name: "When the unit contains a newline it should be rejected", unit: "a\nb", expectedError: true},
		{name: "When the unit contains a space it should be rejected", unit: "a b", expectedError: true},
		{name: "When the unit contains a slash it should be rejected", unit: "../../etc/passwd", expectedError: true},
		{name: "When the unit is not valid UTF-8 it should be rejected", unit: "a\xffb", expectedError: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			err := ValidateJournalUnit(tc.unit)
			if tc.expectedError {
				require.ErrorIs(err, errors.ErrLogInvalidParameters)
				return
			}
			require.NoError(err)
		})
	}
}

func TestJournalPriorityOrdering(t *testing.T) {
	require := require.New(t)

	ordered := []JournalPriority{
		JournalPriorityEmergency,
		JournalPriorityAlert,
		JournalPriorityCritical,
		JournalPriorityError,
		JournalPriorityWarning,
		JournalPriorityNotice,
		JournalPriorityInfo,
		JournalPriorityDebug,
	}
	for i, priority := range ordered {
		level, ok := priority.Level()
		require.True(ok, "priority %q should be defined", priority)
		require.Equal(i, level, "priority %q should map to syslog level %d", priority, i)
	}

	_, ok := JournalPriority("verbose").Level()
	require.False(ok)
}

func TestJournalTickRounding(t *testing.T) {
	testCases := []struct {
		name          string
		input         string
		expectedCeil  string
		expectedFloor string
	}{
		{
			name:          "When the instant is already on a tick it should be left alone in both directions",
			input:         "2026-11-17T09:14:02.481913Z",
			expectedCeil:  "2026-11-17T09:14:02.481913Z",
			expectedFloor: "2026-11-17T09:14:02.481913Z",
		},
		{
			name:          "When the instant carries nanoseconds it should round up for a lower bound and down for an upper bound",
			input:         "2026-11-17T09:14:02.4819135Z",
			expectedCeil:  "2026-11-17T09:14:02.481914Z",
			expectedFloor: "2026-11-17T09:14:02.481913Z",
		},
		{
			name:          "When rounding up crosses a second boundary it should carry into the next second",
			input:         "2026-11-17T09:14:02.9999995Z",
			expectedCeil:  "2026-11-17T09:14:03Z",
			expectedFloor: "2026-11-17T09:14:02.999999Z",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			in := mustTime(t, tc.input)
			require.True(JournalTickCeil(in).Equal(mustTime(t, tc.expectedCeil)), "ceil of %s", tc.input)
			require.True(JournalTickFloor(in).Equal(mustTime(t, tc.expectedFloor)), "floor of %s", tc.input)
		})
	}
}

func TestJournalctlErrorClassification(t *testing.T) {
	testCases := []struct {
		name           string
		stderr         string
		expectedTarget error
	}{
		{
			name:           "When the previous boot holds no data it should be reported as boot unavailable",
			stderr:         "Data from the specified boot (-1) is not available: No such boot ID in journal",
			expectedTarget: errors.ErrLogBootUnavailable,
		},
		{
			name:           "When an older systemd fails the boot lookup it should be reported as boot unavailable",
			stderr:         "Failed to look up boot -1: Cannot assign requested address",
			expectedTarget: errors.ErrLogBootUnavailable,
		},
		{
			name:           "When the journal directory is unreadable it should not be reported as boot unavailable",
			stderr:         "Failed to open files: Permission denied",
			expectedTarget: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().
				ExecuteWithContext(gomock.Any(), journalctlCommand, gomock.Any()).
				Return("", tc.stderr, 1)

			j := NewJournalctl(mockExec, v1beta1.Username(""))
			_, err := j.Output(context.Background())
			require.Error(err)
			if tc.expectedTarget != nil {
				require.ErrorIs(err, tc.expectedTarget)
				return
			}
			require.NotErrorIs(err, errors.ErrLogBootUnavailable)
		})
	}
}

func TestJournalctlWorkingBufferBound(t *testing.T) {
	testCases := []struct {
		name          string
		maxBytes      int
		stdout        string
		expectedError error
		expectedOut   string
	}{
		{
			name:        "When the output fits the working buffer it should be returned verbatim",
			maxBytes:    16,
			stdout:      "0123456789abcdef",
			expectedOut: "0123456789abcdef",
		},
		{
			name:          "When the output overruns the working buffer it should fail rather than return the bytes that fit",
			maxBytes:      16,
			stdout:        "0123456789abcdefg",
			expectedError: errors.ErrLogBufferExceeded,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().
				ExecuteWithBoundedOutputFromDir(gomock.Any(), "", journalctlCommand, gomock.Any(), 2*(tc.maxBytes+1)).
				Return(tc.stdout, "", 0)

			j := NewJournalctl(mockExec, v1beta1.Username(""))
			out, err := j.Output(context.Background(), WithLogMaxOutputBytes(tc.maxBytes))
			if tc.expectedError != nil {
				require.ErrorIs(err, tc.expectedError)
				return
			}
			require.NoError(err)
			require.Equal(tc.expectedOut, out)
		})
	}
}

func TestJournalctlOutputIsVerbatim(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Output must not trim or drop anything: a blank line inside an entry's
	// rendered block is content, and the window is counted over it.
	raw := "Nov 17 09:14:02 dev-01 agent[912]: start\n\n  indented continuation\nNov 17 09:14:03 dev-01 agent[912]: done\n"

	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithContext(gomock.Any(), journalctlCommand, gomock.Any()).
		Return(raw, "", 0)

	j := NewJournalctl(mockExec, v1beta1.Username(""))
	out, err := j.Output(context.Background())
	require.NoError(err)
	require.Equal(raw, out)
}

func TestJournalctlLogsKeepsCatFormat(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().
		ExecuteWithContext(gomock.Any(), journalctlCommand, "--no-pager", "--quiet", "-o", "cat", "-u", "flightctl-agent.service").
		Return("first\n\nsecond \n", "", 0)

	j := NewJournalctl(mockExec, v1beta1.Username(""))
	lines, err := j.Logs(context.Background(), WithLogUnit("flightctl-agent.service"))
	require.NoError(err)
	require.Equal([]string{"first", "second"}, lines)
}
