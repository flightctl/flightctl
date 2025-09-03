package queues

import (
	"context"
	"errors"
	"time"

	"github.com/sirupsen/logrus"
)

// ErrCheckpointMissing indicates that the checkpoint key is missing from Redis
var ErrCheckpointMissing = errors.New("checkpoint key missing from Redis")

type Provider interface {
	NewConsumer(ctx context.Context, queueName string) (Consumer, error)
	NewPublisher(ctx context.Context, queueName string) (Publisher, error)
	ProcessTimedOutMessages(ctx context.Context, queueName string, timeout time.Duration, handler func(entryID string, body []byte) error) (int, error)
	RetryFailedMessages(ctx context.Context, queueName string, config RetryConfig, handler func(entryID string, body []byte, retryCount int) error) (int, error)
	Stop()
	Wait()
	// CheckHealth verifies the provider is operational (e.g. Redis PING)
	CheckHealth(ctx context.Context) error
	// GetLatestProcessedTimestamp returns the latest timestamp that can be safely checkpointed
	// Returns the earliest in-flight task timestamp, or zero time if no in-flight tasks
	GetLatestProcessedTimestamp(ctx context.Context) (time.Time, error)
	// AdvanceCheckpointAndCleanup atomically advances the checkpoint by scanning in-flight tasks
	// and cleans up completed tasks before the checkpoint timestamp
	AdvanceCheckpointAndCleanup(ctx context.Context) error
	// SetCheckpointTimestamp sets the checkpoint to a specific timestamp (for recovery)
	SetCheckpointTimestamp(ctx context.Context, timestamp time.Time) error
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
