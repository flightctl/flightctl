package queues

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

type Provider interface {
	NewQueueConsumer(ctx context.Context, queueName string) (QueueConsumer, error)
	NewQueueProducer(ctx context.Context, queueName string) (QueueProducer, error)
	NewPubSubPublisher(ctx context.Context, channelName string) (PubSubPublisher, error)
	NewPubSubSubscriber(ctx context.Context, channelName string) (PubSubSubscriber, error)
	ProcessTimedOutMessages(ctx context.Context, queueName string, timeout time.Duration, handler func(entryID string, body []byte) error) (int, error)
	RetryFailedMessages(ctx context.Context, queueName string, config RetryConfig, handler func(entryID string, body []byte, retryCount int) error) (int, error)
	Stop()
	Wait()
	// CheckHealth verifies the provider is operational (e.g. Redis PING)
	CheckHealth(ctx context.Context) error
}

type ConsumeHandler func(ctx context.Context, payload []byte, entryID string, consumer QueueConsumer, log logrus.FieldLogger) error

type QueueConsumer interface {
	Consume(ctx context.Context, handler ConsumeHandler) error
	Complete(ctx context.Context, entryID string, body []byte, processingErr error) error
	Close()
}

type QueueProducer interface {
	Enqueue(ctx context.Context, payload []byte, timestamp int64) error
	Close()
}

// PubSubHandler is called when a broadcast message is received
type PubSubHandler func(ctx context.Context, payload []byte, log logrus.FieldLogger) error

// PubSubPublisher sends messages to all active subscribers on a channel
type PubSubPublisher interface {
	Publish(ctx context.Context, payload []byte) error
	Close()
}

// PubSubSubscriber creates subscriptions to receive broadcast messages from a channel
type PubSubSubscriber interface {
	Subscribe(ctx context.Context, handler PubSubHandler) (Subscription, error)
	Close()
}

// Subscription represents an active subscription that can be closed independently
type Subscription interface {
	Close()
}
