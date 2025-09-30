package queues

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishSubscribe(t *testing.T) {
	// This test requires a Redis instance running on localhost:6379
	// Skip if Redis is not available
	provider, err := NewRedisProvider(context.Background(), logrus.New(), "test-process", "localhost", 6379, "", DefaultRetryConfig())
	if err != nil {
		t.Skipf("Redis not available: %v", err)
		return
	}
	defer provider.Stop()
	defer provider.Wait()

	channelName := "test-broadcast-channel"
	testMessage := []byte("Hello, subscribers!")

	// Test scenario: Multiple subscribers receive the same broadcast message
	t.Run("MultipleSubscribers", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Create multiple subscribers
		const numSubscribers = 3
		receivedMessages := make([][]byte, numSubscribers)
		var mu sync.Mutex
		var subscriptions []Subscription

		for i := 0; i < numSubscribers; i++ {
			idx := i
			subscriber, err := provider.NewPubSubSubscriber(ctx, channelName)
			require.NoError(t, err)
			defer subscriber.Close()

			subscription, err := subscriber.Subscribe(ctx, func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
				mu.Lock()
				receivedMessages[idx] = payload
				mu.Unlock()
				return nil
			})
			require.NoError(t, err)
			subscriptions = append(subscriptions, subscription)
		}
		defer func() {
			for _, sub := range subscriptions {
				sub.Close()
			}
		}()

		// Give subscribers time to set up
		time.Sleep(100 * time.Millisecond)

		// Create broadcaster and send message
		broadcaster, err := provider.NewPubSubPublisher(ctx, channelName)
		require.NoError(t, err)
		defer broadcaster.Close()

		err = broadcaster.Publish(ctx, testMessage)
		require.NoError(t, err)

		// Wait a bit for message delivery
		time.Sleep(100 * time.Millisecond)

		// Verify all subscribers received the message
		mu.Lock()
		for i, received := range receivedMessages {
			assert.Equal(t, testMessage, received, "PubSubSubscriber %d should have received the message", i)
		}
		mu.Unlock()
	})

	// Test scenario: Late subscriber doesn't receive old messages
	t.Run("LateSubscriber", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Send a message before any subscribers
		broadcaster, err := provider.NewPubSubPublisher(ctx, channelName+"-late")
		require.NoError(t, err)
		defer broadcaster.Close()

		err = broadcaster.Publish(ctx, []byte("early message"))
		require.NoError(t, err)

		// Wait a bit
		time.Sleep(100 * time.Millisecond)

		// Create subscriber after message was sent
		subscriber, err := provider.NewPubSubSubscriber(ctx, channelName+"-late")
		require.NoError(t, err)
		defer subscriber.Close()

		var receivedMessage []byte
		var messageReceived bool

		subCtx, subCancel := context.WithTimeout(ctx, 1*time.Second)
		defer subCancel()

		subscription, err := subscriber.Subscribe(subCtx, func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
			receivedMessage = payload
			messageReceived = true
			return nil
		})
		require.NoError(t, err)
		defer subscription.Close()

		// Wait for timeout
		time.Sleep(1100 * time.Millisecond)

		// Late subscriber should not have received the early message
		assert.False(t, messageReceived, "Late subscriber should not receive old messages")
		assert.Nil(t, receivedMessage, "Late subscriber should not have any message")
	})

	// Test scenario: Error handling in broadcast handler
	t.Run("HandlerError", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		subscriber, err := provider.NewPubSubSubscriber(ctx, channelName+"-error")
		require.NoError(t, err)
		defer subscriber.Close()

		broadcaster, err := provider.NewPubSubPublisher(ctx, channelName+"-error")
		require.NoError(t, err)
		defer broadcaster.Close()

		subscription, err := subscriber.Subscribe(ctx, func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
			// Return an error to test error handling
			return assert.AnError
		})
		require.NoError(t, err)
		defer subscription.Close()

		// Give subscriber time to set up
		time.Sleep(100 * time.Millisecond)

		// Send a message that will cause handler error
		err = broadcaster.Publish(ctx, []byte("error message"))
		require.NoError(t, err)

		// Wait a bit for message processing
		time.Sleep(100 * time.Millisecond)

		// The test passes if we reach here without hanging
	})
}

func TestBroadcastSubscribeClosedChannel(t *testing.T) {
	provider, err := NewRedisProvider(context.Background(), logrus.New(), "test-process", "localhost", 6379, "", DefaultRetryConfig())
	if err != nil {
		t.Skipf("Redis not available: %v", err)
		return
	}
	defer provider.Stop()
	defer provider.Wait()

	broadcaster, err := provider.NewPubSubPublisher(context.Background(), "test-closed-channel")
	require.NoError(t, err)

	subscriber, err := provider.NewPubSubSubscriber(context.Background(), "test-closed-channel")
	require.NoError(t, err)

	// Close the broadcaster and subscriber
	broadcaster.Close()
	subscriber.Close()

	// Try to use closed broadcaster
	err = broadcaster.Publish(context.Background(), []byte("test"))
	assert.Error(t, err, "Broadcast on closed channel should return error")

	// Try to use closed subscriber
	_, err = subscriber.Subscribe(context.Background(), func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		return nil
	})
	assert.Error(t, err, "Subscribe on closed channel should return error")
}

func TestMultipleSubscriptionsFromSameSubscriber(t *testing.T) {
	provider, err := NewRedisProvider(context.Background(), logrus.New(), "test-process", "localhost", 6379, "", DefaultRetryConfig())
	if err != nil {
		t.Skipf("Redis not available: %v", err)
		return
	}
	defer provider.Stop()
	defer provider.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	subscriber, err := provider.NewPubSubSubscriber(ctx, "test-multiple-subscriptions")
	require.NoError(t, err)
	defer subscriber.Close()

	var receivedCount1, receivedCount2 int32

	// First subscription should succeed
	subscription1, err := subscriber.Subscribe(ctx, func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		atomic.AddInt32(&receivedCount1, 1)
		return nil
	})
	require.NoError(t, err)
	defer subscription1.Close()

	// Second subscription from the same subscriber should also succeed
	subscription2, err := subscriber.Subscribe(ctx, func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		atomic.AddInt32(&receivedCount2, 1)
		return nil
	})
	require.NoError(t, err)
	defer subscription2.Close()

	// Give subscriptions time to establish
	time.Sleep(100 * time.Millisecond)

	// Create broadcaster and send message
	broadcaster, err := provider.NewPubSubPublisher(ctx, "test-multiple-subscriptions")
	require.NoError(t, err)
	defer broadcaster.Close()

	err = broadcaster.Publish(ctx, []byte("test message"))
	require.NoError(t, err)

	// Wait for message delivery
	time.Sleep(100 * time.Millisecond)

	// Both subscriptions should have received the message
	assert.Equal(t, int32(1), atomic.LoadInt32(&receivedCount1), "First subscription should receive message")
	assert.Equal(t, int32(1), atomic.LoadInt32(&receivedCount2), "Second subscription should receive message")
}
