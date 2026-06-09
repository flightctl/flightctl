package log

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/coreos/go-systemd/v22/journal"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

// InitLogs creates and configures a logger with the specified level.
// If no level is provided, defaults to "info".
func InitLogs(level ...string) *logrus.Logger {
	log := logrus.New()
	log.SetReportCaller(true)

	logLevel := "info"
	if len(level) > 0 && level[0] != "" {
		logLevel = level[0]
	}

	parsedLevel, err := logrus.ParseLevel(logLevel)
	if err != nil {
		parsedLevel = logrus.InfoLevel
	}
	log.SetLevel(parsedLevel)

	return log
}

// WithReqIDFromCtx create logger with request id from the context, request id is set by middleware.RequestID
func WithReqIDFromCtx(ctx context.Context, inner logrus.FieldLogger) logrus.FieldLogger {
	return inner.WithField("request_id", middleware.GetReqID(ctx))
}

// PrefixLogger is wrapper around a logrus with an optional prefix
type PrefixLogger struct {
	*logrus.Logger
	prefix string
}

// NewPrefixLogger creates a new PrefixLogger
func NewPrefixLogger(prefix string) *PrefixLogger {
	logger := logrus.New()
	logger.SetReportCaller(true)
	logger.SetFormatter(&PrefixFormatter{
		Prefix:     prefix,
		CallLevels: 3,
	})

	return &PrefixLogger{
		logger,
		prefix,
	}
}

// Prefix returns the prefix of the logger
func (l *PrefixLogger) Prefix() string {
	return l.prefix
}

func (p *PrefixLogger) Level(level string) {
	parsedLevel, err := logrus.ParseLevel(level)
	if err != nil {
		parsedLevel = logrus.InfoLevel
	}
	p.SetLevel(parsedLevel)
}

type PrefixFormatter struct {
	Prefix     string
	CallLevels int
	// journaldDetected is cached to avoid repeated syscalls
	journaldDetected     bool
	journaldDetectedOnce sync.Once
}

// isJournaldConnected checks if stderr is connected to systemd journald.
// The result is cached after the first check.
func (f *PrefixFormatter) isJournaldConnected() bool {
	f.journaldDetectedOnce.Do(func() {
		// Check if stderr is connected to journald's stream transport
		connected, err := journal.StderrIsJournalStream()
		if err == nil && connected {
			f.journaldDetected = true
		}
	})
	return f.journaldDetected
}

// logrusLevelToSyslogPriority maps logrus log levels to syslog priority values
// per RFC 5424. These priorities are used in the sd-daemon protocol for journald.
// See: https://man7.org/linux/man-pages/man3/sd-daemon.3.html
func logrusLevelToSyslogPriority(level logrus.Level) int {
	switch level {
	case logrus.PanicLevel:
		return 0 // Emergency
	case logrus.FatalLevel:
		return 2 // Critical
	case logrus.ErrorLevel:
		return 3 // Error
	case logrus.WarnLevel:
		return 4 // Warning
	case logrus.InfoLevel:
		return 6 // Informational
	case logrus.DebugLevel, logrus.TraceLevel:
		return 7 // Debug
	default:
		return 6 // Default to Info
	}
}

func (f *PrefixFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	// ref. https://stackoverflow.com/questions/1760757/how-to-efficiently-concatenate-strings-in-go
	var sb strings.Builder

	// Add syslog priority prefix if connected to systemd journald (EDM-4119)
	// This allows journalctl to filter logs by priority correctly
	if f.isJournaldConnected() {
		priority := logrusLevelToSyslogPriority(entry.Level)
		sb.WriteString("<")
		sb.WriteString(strconv.Itoa(priority))
		sb.WriteString(">")
	}

	// timestamp (RFC3339)
	sb.WriteString(`time="`)
	sb.WriteString(entry.Time.Format("2006-01-02T15:04:05.000000Z"))
	sb.WriteString(`" `)

	// log level
	sb.WriteString(`level=`)
	sb.WriteString(entry.Level.String())
	sb.WriteString(" ")

	// message
	sb.WriteString(`msg="`)
	// prefix
	if f.Prefix != "" {
		sb.WriteString(f.Prefix)
		sb.WriteString(": ")
	}
	sb.WriteString(entry.Message)
	sb.WriteString(`" `)

	if err, ok := entry.Data[logrus.ErrorKey]; ok {
		sb.WriteString(`err="`)
		sb.WriteString(err.(error).Error())
		sb.WriteString(`" `)
	}

	// caller if available and not an info level log
	if entry.HasCaller() && entry.Level != logrus.InfoLevel {
		sb.WriteString(`file="`)
		sb.WriteString(trimCallerLevels(entry.Caller.File, 3))
		sb.WriteString(":")
		sb.WriteString(strconv.Itoa(entry.Caller.Line))
		sb.WriteString(`"`)
	}
	sb.WriteString("\n")

	return []byte(sb.String()), nil
}

func trimCallerLevels(path string, levels int) string {
	sep := "/"

	// count the number of '/' in the full path string starting from the end
	count := 0
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == sep[0] {
			count++
			if count == levels {
				return path[i+1:]
			}
		}
	}

	// path is already shorter than levels
	return path
}

// Truncate truncates a string to the given limit or first newline and adds "..." at the end
func Truncate(msg string, limit int) string {
	truncIdx := strings.Index(msg, "\n")
	if truncIdx == -1 || truncIdx > limit {
		if len(msg) > limit {
			truncIdx = limit
		} else {
			return msg
		}
	}
	return msg[:truncIdx] + "..."
}
