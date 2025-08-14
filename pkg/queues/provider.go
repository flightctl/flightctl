package queues

import (
	"context"

	"github.com/sirupsen/logrus"
)

type Provider interface {
	NewConsumer(queueName string) (Consumer, error)
	NewPublisher(queueName string) (Publisher, error)
	NewBroadcaster(channelName string) (Broadcaster, error)
	NewSubscriber(channelName string) (Subscriber, error)
	Stop()
	Wait()
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

// BroadcastHandler is called when a broadcast message is received
type BroadcastHandler func(ctx context.Context, payload []byte, log logrus.FieldLogger) error

// Broadcaster sends messages to all active subscribers on a channel
type Broadcaster interface {
	Broadcast(ctx context.Context, payload []byte) error
	Close()
}

// Subscriber creates subscriptions to receive broadcast messages from a channel
type Subscriber interface {
	Subscribe(ctx context.Context, handler BroadcastHandler) (Subscription, error)
	Close()
}

// Subscription represents an active subscription that can be closed independently
type Subscription interface {
	Close()
}
