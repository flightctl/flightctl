package queues

import (
	"context"

	"github.com/sirupsen/logrus"
)

type Provider interface {
	NewQueueConsumer(queueName string) (QueueConsumer, error)
	NewQueueProducer(queueName string) (QueueProducer, error)
	NewPubSubPublisher(channelName string) (PubSubPublisher, error)
	NewPubSubSubscriber(channelName string) (PubSubSubscriber, error)
	Stop()
	Wait()
}

type ConsumeHandler func(ctx context.Context, payload []byte, log logrus.FieldLogger) error

type QueueConsumer interface {
	Consume(ctx context.Context, handler ConsumeHandler) error
	Close()
}

type QueueProducer interface {
	Enqueue(ctx context.Context, payload []byte) error
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
