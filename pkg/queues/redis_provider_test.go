package queues

import (
	"context"
	"errors"
	"testing"

	"github.com/go-redis/redismock/v9"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func TestRedisQueue_ackAndDeleteMessage(t *testing.T) {
	client, mock := redismock.NewClientMock()

	queue := &redisQueue{
		client:     client,
		name:       "test-queue",
		log:        logrus.New(),
		consumerID: "test-consumer",
	}

	ctx := context.Background()
	messageID := "1234567890-0"

	t.Run("successful ack and delete", func(t *testing.T) {
		mock.ExpectXAck("test-queue", "test-queue", messageID).SetVal(1)
		mock.ExpectXDel("test-queue", messageID).SetVal(1)

		err := queue.ackAndDeleteMessage(ctx, messageID)
		assert.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("ack fails", func(t *testing.T) {
		mock.ExpectXAck("test-queue", "test-queue", messageID).SetErr(errors.New("ack error"))

		err := queue.ackAndDeleteMessage(ctx, messageID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to acknowledge message")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("delete fails after successful ack", func(t *testing.T) {
		mock.ExpectXAck("test-queue", "test-queue", messageID).SetVal(1)
		mock.ExpectXDel("test-queue", messageID).SetErr(errors.New("delete error"))

		err := queue.ackAndDeleteMessage(ctx, messageID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to delete message")
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestRedisQueue_consumeOnce_MessageLifecycle(t *testing.T) {
	client, mock := redismock.NewClientMock()

	queue := &redisQueue{
		client:     client,
		name:       "test-queue",
		log:        logrus.New(),
		consumerID: "test-consumer",
	}

	ctx := context.Background()
	messageID := "1234567890-0"
	body := []byte("test message")

	t.Run("successful message processing with ack and delete", func(t *testing.T) {
		mock.ExpectXReadGroup(&redis.XReadGroupArgs{
			Group:    "test-queue",
			Consumer: "test-consumer",
			Streams:  []string{"test-queue", ">"},
			Count:    1,
			Block:    0,
		}).SetVal([]redis.XStream{
			{
				Stream: "test-queue",
				Messages: []redis.XMessage{
					{
						ID: messageID,
						Values: map[string]interface{}{
							"body": string(body),
						},
					},
				},
			},
		})

		mock.ExpectXAck("test-queue", "test-queue", messageID).SetVal(1)
		mock.ExpectXDel("test-queue", messageID).SetVal(1)

		handlerCalled := false
		handler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
			handlerCalled = true
			assert.Equal(t, body, payload)
			return nil
		}

		err := queue.consumeOnce(ctx, handler)
		assert.NoError(t, err)
		assert.True(t, handlerCalled)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("handler error still acks and deletes message", func(t *testing.T) {
		handlerError := errors.New("handler failed")

		mock.ExpectXReadGroup(&redis.XReadGroupArgs{
			Group:    "test-queue",
			Consumer: "test-consumer",
			Streams:  []string{"test-queue", ">"},
			Count:    1,
			Block:    0,
		}).SetVal([]redis.XStream{
			{
				Stream: "test-queue",
				Messages: []redis.XMessage{
					{
						ID: messageID,
						Values: map[string]interface{}{
							"body": string(body),
						},
					},
				},
			},
		})

		mock.ExpectXAck("test-queue", "test-queue", messageID).SetVal(1)
		mock.ExpectXDel("test-queue", messageID).SetVal(1)

		handler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
			return handlerError
		}

		err := queue.consumeOnce(ctx, handler)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "handler error")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("bad message type acks and deletes", func(t *testing.T) {
		mock.ExpectXReadGroup(&redis.XReadGroupArgs{
			Group:    "test-queue",
			Consumer: "test-consumer",
			Streams:  []string{"test-queue", ">"},
			Count:    1,
			Block:    0,
		}).SetVal([]redis.XStream{
			{
				Stream: "test-queue",
				Messages: []redis.XMessage{
					{
						ID: messageID,
						Values: map[string]interface{}{
							"body": 12345, // Invalid (should be string or []byte)
						},
					},
				},
			},
		})

		mock.ExpectXAck("test-queue", "test-queue", messageID).SetVal(1)
		mock.ExpectXDel("test-queue", messageID).SetVal(1)

		handlerCalled := false
		handler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
			handlerCalled = true
			return nil
		}

		err := queue.consumeOnce(ctx, handler)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "handler errors")
		assert.False(t, handlerCalled) // Handler should not be called for bad message
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("ack/delete failure after successful handler", func(t *testing.T) {
		mock.ExpectXReadGroup(&redis.XReadGroupArgs{
			Group:    "test-queue",
			Consumer: "test-consumer",
			Streams:  []string{"test-queue", ">"},
			Count:    1,
			Block:    0,
		}).SetVal([]redis.XStream{
			{
				Stream: "test-queue",
				Messages: []redis.XMessage{
					{
						ID: messageID,
						Values: map[string]interface{}{
							"body": string(body),
						},
					},
				},
			},
		})

		mock.ExpectXAck("test-queue", "test-queue", messageID).SetErr(errors.New("ack failed"))

		handlerCalled := false
		handler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
			handlerCalled = true
			return nil
		}

		err := queue.consumeOnce(ctx, handler)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to purge")
		assert.True(t, handlerCalled) // Handler should still be called
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestRedisQueue_consumeOnce_NoMessages(t *testing.T) {
	client, mock := redismock.NewClientMock()

	queue := &redisQueue{
		client:     client,
		name:       "test-queue",
		log:        logrus.New(),
		consumerID: "test-consumer",
	}

	ctx := context.Background()

	t.Run("no messages available", func(t *testing.T) {
		mock.ExpectXReadGroup(&redis.XReadGroupArgs{
			Group:    "test-queue",
			Consumer: "test-consumer",
			Streams:  []string{"test-queue", ">"},
			Count:    1,
			Block:    0,
		}).SetVal([]redis.XStream{})

		handlerCalled := false
		handler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
			handlerCalled = true
			return nil
		}

		err := queue.consumeOnce(ctx, handler)
		assert.NoError(t, err)
		assert.False(t, handlerCalled)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("redis read error", func(t *testing.T) {
		mock.ExpectXReadGroup(&redis.XReadGroupArgs{
			Group:    "test-queue",
			Consumer: "test-consumer",
			Streams:  []string{"test-queue", ">"},
			Count:    1,
			Block:    0,
		}).SetErr(errors.New("redis connection failed"))

		handlerCalled := false
		handler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
			handlerCalled = true
			return nil
		}

		err := queue.consumeOnce(ctx, handler)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read from stream")
		assert.False(t, handlerCalled)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}
