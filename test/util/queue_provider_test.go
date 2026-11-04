package util

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestTestProviderNamedQueueIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := NewTestProvider(logrus.New())
	t.Cleanup(func() {
		provider.Stop()
		cancel()
		provider.Wait()
	})
	mainGate, releaseMain := context.WithCancel(ctx)
	defer releaseMain()
	mainProducer, err := provider.NewQueueProducer(ctx, consts.TaskQueue)
	require.NoError(t, err)
	labelProducer, err := provider.NewQueueProducer(ctx, consts.DeviceLabelSyncTaskQueue)
	require.NoError(t, err)
	require.NoError(t, mainProducer.Enqueue(ctx, []byte("main-first"), 0))
	require.NoError(t, mainProducer.Enqueue(ctx, []byte("main-second"), 0))
	require.NoError(t, labelProducer.Enqueue(ctx, []byte("label-sync"), 0))

	mainReceived := make(chan string, 2)
	mainConsumer, err := provider.NewQueueConsumer(ctx, consts.TaskQueue)
	require.NoError(t, err)
	require.NoError(t, mainConsumer.Consume(ctx, func(_ context.Context, payload []byte, _ string, _ queues.QueueConsumer, _ logrus.FieldLogger) error {
		mainReceived <- string(payload)
		if string(payload) == "main-first" {
			<-mainGate.Done()
		}
		return nil
	}))
	require.Equal(t, "main-first", receiveQueueMessage(t, ctx, mainReceived))

	labelReceived := make(chan string, 3)
	labelConsumer, err := provider.NewQueueConsumer(ctx, consts.DeviceLabelSyncTaskQueue)
	require.NoError(t, err)
	require.NoError(t, labelConsumer.Consume(ctx, func(_ context.Context, payload []byte, _ string, _ queues.QueueConsumer, _ logrus.FieldLogger) error {
		labelReceived <- string(payload)
		return nil
	}))
	require.Equal(t, "label-sync", receiveQueueMessage(t, ctx, labelReceived))
	releaseMain()
	require.Equal(t, "main-second", receiveQueueMessage(t, ctx, mainReceived))
}

func TestTestProviderConcurrentQueueCreation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := NewTestProvider(logrus.New())
	t.Cleanup(func() {
		provider.Stop()
		cancel()
		provider.Wait()
	})
	const producersPerQueue = 10
	names := []string{consts.TaskQueue, consts.DeviceLabelSyncTaskQueue, consts.DeltaGenerationTaskQueue}
	errors := make(chan error, len(names)*producersPerQueue)
	for _, name := range names {
		for i := 0; i < producersPerQueue; i++ {
			go func() {
				producer, err := provider.NewQueueProducer(ctx, name)
				if err == nil {
					err = producer.Enqueue(ctx, []byte(name), 0)
				}
				errors <- err
			}()
		}
	}
	for i := 0; i < len(names)*producersPerQueue; i++ {
		select {
		case err := <-errors:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("timed out creating queue producers")
		}
	}
	for _, name := range names {
		received := make(chan string, len(names)*producersPerQueue)
		consumer, err := provider.NewQueueConsumer(ctx, name)
		require.NoError(t, err)
		require.NoError(t, consumer.Consume(ctx, func(_ context.Context, payload []byte, _ string, _ queues.QueueConsumer, _ logrus.FieldLogger) error {
			received <- string(payload)
			return nil
		}))
		for i := 0; i < producersPerQueue; i++ {
			require.Equal(t, name, receiveQueueMessage(t, ctx, received))
		}
	}
}

func TestTestProviderNamedQueueShutdown(t *testing.T) {
	for _, name := range []string{consts.TaskQueue, consts.DeviceLabelSyncTaskQueue} {
		t.Run("When "+name+" stops it should drain buffered work and drop subsequent messages", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			provider := NewTestProvider(logrus.New())
			t.Cleanup(func() {
				provider.Stop()
				cancel()
				provider.Wait()
			})
			gate, release := context.WithCancel(ctx)
			defer release()
			consumer, err := provider.NewQueueConsumer(ctx, name)
			require.NoError(t, err)
			producer, err := provider.NewQueueProducer(ctx, name)
			require.NoError(t, err)
			received := make(chan string, 2)
			require.NoError(t, consumer.Consume(ctx, func(_ context.Context, payload []byte, _ string, _ queues.QueueConsumer, _ logrus.FieldLogger) error {
				received <- string(payload)
				<-gate.Done()
				return nil
			}))
			require.NoError(t, producer.Enqueue(ctx, []byte("in-flight"), 0))
			require.Equal(t, "in-flight", receiveQueueMessage(t, ctx, received))
			require.NoError(t, producer.Enqueue(ctx, []byte("buffered"), 0))
			provider.Stop()
			release()
			require.Equal(t, "buffered", receiveQueueMessage(t, ctx, received))
			for i := 0; i < 100; i++ {
				require.NoError(t, producer.Enqueue(ctx, []byte("dropped"), 0))
			}
		})
	}
}

func receiveQueueMessage(t *testing.T, ctx context.Context, messages <-chan string) string {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-ctx.Done():
		t.Fatal("timed out waiting for a queue message")
		return ""
	}
}
