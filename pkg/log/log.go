package log

import (
	"context"

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
	logger *logrus.Logger
	prefix string
}

// NewPrefixLogger creates a new PrefixLogger
func NewPrefixLogger(prefix string) *PrefixLogger {
	logger := logrus.New()
	logger.SetReportCaller(true)

	return &PrefixLogger{
		logger: logger,
		prefix: prefix,
	}
}

func (p *PrefixLogger) Info(args ...interface{}) {
	p.logger.Info(p.prependPrefix(args[0].(string)))
}

func (p *PrefixLogger) Infof(format string, args ...interface{}) {
	p.logger.Infof(p.prependPrefix(format), args...)
}

func (p *PrefixLogger) Error(args ...interface{}) {
	p.logger.Error(p.prependPrefix(args[0].(string)))
}

func (p *PrefixLogger) Errorf(format string, args ...interface{}) {
	p.logger.Errorf(p.prependPrefix(format), args...)
}

func (p *PrefixLogger) Debug(args ...interface{}) {
	p.logger.Debug(p.prependPrefix(args[0].(string)))
}

func (p *PrefixLogger) Debugf(format string, args ...interface{}) {
	p.logger.Debugf(p.prependPrefix(format), args...)
}

func (p *PrefixLogger) Warn(args ...interface{}) {
	p.logger.Warn(p.prependPrefix(args[0].(string)))
}

func (p *PrefixLogger) Warnf(format string, args ...interface{}) {
	p.logger.Warnf(p.prependPrefix(format), args...)
}

func (p *PrefixLogger) Fatal(args ...interface{}) {
	p.logger.Fatal(p.prependPrefix(args[0].(string)))
}

func (p *PrefixLogger) Fatalf(format string, args ...interface{}) {
	p.logger.Fatalf(p.prependPrefix(format), args...)
}

func (p *PrefixLogger) SetLevel(level string) {
	parsedLevel, err := logrus.ParseLevel(level)
	if err != nil {
		parsedLevel = logrus.InfoLevel
	}
	p.logger.SetLevel(parsedLevel)
}

func (p *PrefixLogger) Prefix() string {
	return p.prefix
}

// prependPrefix checks if a prefix is set and prepends it to the message
func (p *PrefixLogger) prependPrefix(msg string) string {
	if p.prefix != "" {
		return p.prefix + ": " + msg
	}
	return msg
}
