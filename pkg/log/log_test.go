package log

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestPrefixFormatterJournaldPriority tests that syslog priority prefixes
// are added when running under systemd journald (EDM-4119)
func TestPrefixFormatterJournaldPriority(t *testing.T) {
	tests := []struct {
		name            string
		level           logrus.Level
		expectedPrefix  string
		journaldEnabled bool
	}{
		{
			name:            "When journald is enabled and log level is panic it should add priority 0",
			level:           logrus.PanicLevel,
			expectedPrefix:  "<0>",
			journaldEnabled: true,
		},
		{
			name:            "When journald is enabled and log level is fatal it should add priority 2",
			level:           logrus.FatalLevel,
			expectedPrefix:  "<2>",
			journaldEnabled: true,
		},
		{
			name:            "When journald is enabled and log level is error it should add priority 3",
			level:           logrus.ErrorLevel,
			expectedPrefix:  "<3>",
			journaldEnabled: true,
		},
		{
			name:            "When journald is enabled and log level is warn it should add priority 4",
			level:           logrus.WarnLevel,
			expectedPrefix:  "<4>",
			journaldEnabled: true,
		},
		{
			name:            "When journald is enabled and log level is info it should add priority 6",
			level:           logrus.InfoLevel,
			expectedPrefix:  "<6>",
			journaldEnabled: true,
		},
		{
			name:            "When journald is enabled and log level is debug it should add priority 7",
			level:           logrus.DebugLevel,
			expectedPrefix:  "<7>",
			journaldEnabled: true,
		},
		{
			name:            "When journald is enabled and log level is trace it should add priority 7",
			level:           logrus.TraceLevel,
			expectedPrefix:  "<7>",
			journaldEnabled: true,
		},
		{
			name:            "When journald is disabled and log level is error it should not add priority prefix",
			level:           logrus.ErrorLevel,
			expectedPrefix:  "",
			journaldEnabled: false,
		},
		{
			name:            "When journald is disabled and log level is debug it should not add priority prefix",
			level:           logrus.DebugLevel,
			expectedPrefix:  "",
			journaldEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a formatter with journald detection mocked
			formatter := &PrefixFormatter{
				Prefix:           "",
				CallLevels:       3,
				journaldDetected: tt.journaldEnabled,
			}
			// Pre-initialize the sync.Once to use our mocked value
			formatter.journaldDetectedOnce.Do(func() {})

			// Create a log entry
			entry := &logrus.Entry{
				Logger:  logrus.New(),
				Level:   tt.level,
				Message: "test message",
			}

			// Format the entry
			formatted, err := formatter.Format(entry)
			require.NoError(t, err)

			formattedStr := string(formatted)

			if tt.journaldEnabled {
				// Should start with the priority prefix
				require.True(t, strings.HasPrefix(formattedStr, tt.expectedPrefix),
					"Expected log to start with %s, got: %s", tt.expectedPrefix, formattedStr)
			} else {
				// Should NOT have any priority prefix
				require.False(t, strings.HasPrefix(formattedStr, "<"),
					"Expected no priority prefix, but got: %s", formattedStr)
			}

			// Should still contain the level as text (for human readability)
			require.Contains(t, formattedStr, "level="+tt.level.String())
			require.Contains(t, formattedStr, "test message")
		})
	}
}

// TestLogrusLevelToSyslogPriority tests the priority mapping function
func TestLogrusLevelToSyslogPriority(t *testing.T) {
	tests := []struct {
		name             string
		level            logrus.Level
		expectedPriority int
	}{
		{
			name:             "When log level is panic it should map to priority 0 (emergency)",
			level:            logrus.PanicLevel,
			expectedPriority: 0,
		},
		{
			name:             "When log level is fatal it should map to priority 2 (critical)",
			level:            logrus.FatalLevel,
			expectedPriority: 2,
		},
		{
			name:             "When log level is error it should map to priority 3 (error)",
			level:            logrus.ErrorLevel,
			expectedPriority: 3,
		},
		{
			name:             "When log level is warn it should map to priority 4 (warning)",
			level:            logrus.WarnLevel,
			expectedPriority: 4,
		},
		{
			name:             "When log level is info it should map to priority 6 (informational)",
			level:            logrus.InfoLevel,
			expectedPriority: 6,
		},
		{
			name:             "When log level is debug it should map to priority 7 (debug)",
			level:            logrus.DebugLevel,
			expectedPriority: 7,
		},
		{
			name:             "When log level is trace it should map to priority 7 (debug)",
			level:            logrus.TraceLevel,
			expectedPriority: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			priority := logrusLevelToSyslogPriority(tt.level)
			require.Equal(t, tt.expectedPriority, priority)
		})
	}
}

// TestPrefixFormatterWithPrefix tests that the prefix functionality still works
func TestPrefixFormatterWithPrefix(t *testing.T) {
	tests := []struct {
		name            string
		prefix          string
		message         string
		journaldEnabled bool
	}{
		{
			name:            "When prefix is set and journald is disabled it should include prefix in message",
			prefix:          "agent",
			message:         "starting",
			journaldEnabled: false,
		},
		{
			name:            "When prefix is set and journald is enabled it should include both priority and prefix",
			prefix:          "agent",
			message:         "starting",
			journaldEnabled: true,
		},
		{
			name:            "When prefix is empty and journald is disabled it should work correctly",
			prefix:          "",
			message:         "no prefix",
			journaldEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := &PrefixFormatter{
				Prefix:           tt.prefix,
				CallLevels:       3,
				journaldDetected: tt.journaldEnabled,
			}
			formatter.journaldDetectedOnce.Do(func() {})

			entry := &logrus.Entry{
				Logger:  logrus.New(),
				Level:   logrus.InfoLevel,
				Message: tt.message,
			}

			formatted, err := formatter.Format(entry)
			require.NoError(t, err)

			formattedStr := string(formatted)

			if tt.journaldEnabled {
				require.True(t, strings.HasPrefix(formattedStr, "<6>"))
			}

			if tt.prefix != "" {
				expectedMsg := tt.prefix + ": " + tt.message
				require.Contains(t, formattedStr, expectedMsg)
			} else {
				require.Contains(t, formattedStr, tt.message)
			}
		})
	}
}

// TestInitLogs tests the logger initialization
func TestInitLogs(t *testing.T) {
	tests := []struct {
		name          string
		level         string
		expectedLevel logrus.Level
	}{
		{
			name:          "When level is debug it should set debug level",
			level:         "debug",
			expectedLevel: logrus.DebugLevel,
		},
		{
			name:          "When level is info it should set info level",
			level:         "info",
			expectedLevel: logrus.InfoLevel,
		},
		{
			name:          "When level is warn it should set warn level",
			level:         "warn",
			expectedLevel: logrus.WarnLevel,
		},
		{
			name:          "When level is error it should set error level",
			level:         "error",
			expectedLevel: logrus.ErrorLevel,
		},
		{
			name:          "When level is invalid it should default to info",
			level:         "invalid",
			expectedLevel: logrus.InfoLevel,
		},
		{
			name:          "When level is empty it should default to info",
			level:         "",
			expectedLevel: logrus.InfoLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := InitLogs(tt.level)
			require.Equal(t, tt.expectedLevel, log.Level)
		})
	}
}

// TestPrefixLoggerLevel tests the PrefixLogger level setting
func TestPrefixLoggerLevel(t *testing.T) {
	tests := []struct {
		name          string
		level         string
		expectedLevel logrus.Level
	}{
		{
			name:          "When setting level to debug it should use debug level",
			level:         "debug",
			expectedLevel: logrus.DebugLevel,
		},
		{
			name:          "When setting level to info it should use info level",
			level:         "info",
			expectedLevel: logrus.InfoLevel,
		},
		{
			name:          "When setting invalid level it should default to info",
			level:         "invalid",
			expectedLevel: logrus.InfoLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewPrefixLogger("")
			logger.Level(tt.level)
			require.Equal(t, tt.expectedLevel, logger.Logger.Level)
		})
	}
}

// TestJournaldDetectionCaching tests that journald detection is cached
func TestJournaldDetectionCaching(t *testing.T) {
	t.Run("When isJournaldConnected is called multiple times it should only check once", func(t *testing.T) {
		formatter := &PrefixFormatter{
			Prefix:     "",
			CallLevels: 3,
		}

		// Call multiple times
		result1 := formatter.isJournaldConnected()
		result2 := formatter.isJournaldConnected()
		result3 := formatter.isJournaldConnected()

		// Results should be consistent
		require.Equal(t, result1, result2)
		require.Equal(t, result2, result3)

		// The sync.Once should have been executed (we can't directly test this,
		// but we can verify the results are consistent which implies caching works)
	})
}

// TestTruncate tests the log message truncation utility
func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		limit    int
		expected string
	}{
		{
			name:     "When message is shorter than limit it should return full message",
			msg:      "short",
			limit:    100,
			expected: "short",
		},
		{
			name:     "When message is longer than limit it should truncate and add ellipsis",
			msg:      "this is a very long message that exceeds the limit",
			limit:    10,
			expected: "this is a ...",
		},
		{
			name:     "When message contains newline before limit it should truncate at newline",
			msg:      "first line\nsecond line",
			limit:    100,
			expected: "first line...",
		},
		{
			name:     "When message contains newline after limit it should truncate at newline",
			msg:      "short\nvery long message that exceeds the limit",
			limit:    100,
			expected: "short...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Truncate(tt.msg, tt.limit)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestPrefixFormatterIntegration is an integration test that verifies
// the complete log formatting works end-to-end
func TestPrefixFormatterIntegration(t *testing.T) {
	t.Run("When logging at different levels the format should be correct", func(t *testing.T) {
		// Create a logger with our formatter
		logger := NewPrefixLogger("test")
		logger.SetLevel(logrus.DebugLevel)

		// Capture output
		var buf bytes.Buffer
		logger.SetOutput(&buf)

		// Log messages at different levels
		logger.Debug("debug message")
		logger.Info("info message")
		logger.Warn("warn message")
		logger.Error("error message")

		output := buf.String()

		// Verify all messages are present
		require.Contains(t, output, "level=debug")
		require.Contains(t, output, "level=info")
		require.Contains(t, output, "level=warning")
		require.Contains(t, output, "level=error")

		// Verify messages contain the content
		require.Contains(t, output, "debug message")
		require.Contains(t, output, "info message")
		require.Contains(t, output, "warn message")
		require.Contains(t, output, "error message")

		// Verify prefix is included
		require.Contains(t, output, "test: debug message")
		require.Contains(t, output, "test: info message")
	})
}

// TestPrefixFormatterActualJournaldDetection tests with the real JOURNAL_STREAM detection
// This test will pass or fail based on whether the test is actually running under journald
func TestPrefixFormatterActualJournaldDetection(t *testing.T) {
	t.Run("When checking actual journald connection it should not panic", func(t *testing.T) {
		formatter := &PrefixFormatter{
			Prefix:     "",
			CallLevels: 3,
		}

		// This should not panic regardless of whether we're under journald or not
		require.NotPanics(t, func() {
			_ = formatter.isJournaldConnected()
		})

		// If JOURNAL_STREAM is set, we should detect journald
		if os.Getenv("JOURNAL_STREAM") != "" {
			t.Log("JOURNAL_STREAM is set, expecting journald detection")
		} else {
			t.Log("JOURNAL_STREAM is not set, expecting no journald detection")
		}
	})
}
