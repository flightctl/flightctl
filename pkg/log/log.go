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
