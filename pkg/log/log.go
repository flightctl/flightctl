package log

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

func InitLogs() *logrus.Logger {
	log := logrus.New()

	log.SetReportCaller(true)

	return log
}

// WithReqIDFromCtx create logger with request id from the context, request id is set by middleware.RequestID
func WithReqIDFromCtx(ctx context.Context, inner logrus.FieldLogger) logrus.FieldLogger {
	return inner.WithField("request_id", middleware.GetReqID(ctx))
}

func WithReqID(reqID string, inner logrus.FieldLogger) logrus.FieldLogger {
	return inner.WithField("request_id", reqID)
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
}

func (f *PrefixFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	// ref. https://stackoverflow.com/questions/1760757/how-to-efficiently-concatenate-strings-in-go
	var sb strings.Builder

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

	// caller if available
	if entry.HasCaller() {
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
