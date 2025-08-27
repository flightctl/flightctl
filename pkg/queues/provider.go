package queues

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

type Provider interface {
	NewConsumer(ctx context.Context, queueName string) (Consumer, error)
	NewPublisher(ctx context.Context, queueName string) (Publisher, error)
	ProcessTimedOutMessages(ctx context.Context, queueName string, timeout time.Duration, handler func(entryID string, body []byte) error) (int, error)
	RetryFailedMessages(ctx context.Context, queueName string, config RetryConfig) (int, error)
	Stop()
	Wait()
	// CheckHealth verifies the provider is operational (e.g. Redis PING)
	CheckHealth(ctx context.Context) error
}

type ConsumeHandler func(ctx context.Context, payload []byte, entryID string, consumer Consumer, log logrus.FieldLogger) error

type Consumer interface {
	Consume(ctx context.Context, handler ConsumeHandler) error
	Complete(ctx context.Context, entryID string, body []byte, processingErr error) error
	Close()
}

type Publisher interface {
	Publish(ctx context.Context, payload []byte, timestamp int64) error
	Close()
}
