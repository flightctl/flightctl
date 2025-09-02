package queues

import (
	"context"

	"github.com/sirupsen/logrus"
)

type Provider interface {
	NewConsumer(queueName string) (Consumer, error)
	NewPublisher(queueName string) (Publisher, error)
	Stop()
	Wait()
	// CheckHealth verifies the provider is operational (e.g. Redis PING)
	CheckHealth(ctx context.Context) error
}

type ConsumeHandler func(ctx context.Context, payload []byte, log logrus.FieldLogger) error

type Consumer interface {
	Consume(ctx context.Context, handler ConsumeHandler) error
	Close()
}

type Publisher interface {
	Publish(ctx context.Context, payload []byte) error
	Close()
}
