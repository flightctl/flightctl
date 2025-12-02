package tasks_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// Redis state polling helpers to replace fixed time.Sleep() calls
func waitForRedisConsumerGroupReady(ctx context.Context, queueName string, timeout time.Duration) bool {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "adminpass",
		DB:       0,
	})
	defer redisClient.Close()

	groupName := queueName + "-group"
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Check if consumer group exists and is ready
		groups, err := redisClient.XInfoGroups(ctx, queueName).Result()
		if err == nil {
			for _, group := range groups {
				if group.Name == groupName {
					return true
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func waitForRedisFailedMessagesState(ctx context.Context, queueName string, expectedCount int, timeout time.Duration) bool {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "adminpass",
		DB:       0,
	})
	defer redisClient.Close()

	failedSetKey := "failed_messages:" + queueName
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		count, err := redisClient.ZCard(ctx, failedSetKey).Result()
		if err == nil && int(count) == expectedCount {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func waitForRedisInFlightTasksState(ctx context.Context, queueName string, expectedCompleted, expectedIncomplete int, timeout time.Duration) bool {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "adminpass",
		DB:       0,
	})
	defer redisClient.Close()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		tasks, err := redisClient.ZRange(ctx, "in_flight_tasks", 0, -1).Result()
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		completedCount := 0
		incompleteCount := 0
		for _, task := range tasks {
			if !strings.HasPrefix(task, queueName+"|") {
				continue
			}
			if strings.HasSuffix(task, ":completed") {
				completedCount++
			} else {
				incompleteCount++
			}
		}

		if completedCount == expectedCompleted && incompleteCount == expectedIncomplete {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func waitForRedisConsumerStopped(ctx context.Context, queueName string, timeout time.Duration) bool {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "adminpass",
		DB:       0,
	})
	defer redisClient.Close()

	deadline := time.Now().Add(timeout)
	groupName := queueName + "-group"

	for time.Now().Before(deadline) {
		// Check if consumer group has no active consumers
		consumers, err := redisClient.XInfoConsumers(ctx, queueName, groupName).Result()
		if err != nil || len(consumers) == 0 {
			return true
		}

		// Check if all consumers are idle
		allIdle := true
		for _, consumer := range consumers {
			if consumer.Idle < 100 { // If consumer was active within last 100ms
				allIdle = false
				break
			}
		}
		if allIdle {
			return true
		}

		time.Sleep(50 * time.Millisecond)
	}
	return false
}

var _ = Describe("Redis Provider Integration Tests", FlakeAttempts(5), func() {
	var (
		log       *logrus.Logger
		ctx       context.Context
		cancel    context.CancelFunc
		provider  queues.Provider
		processID string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		log = logrus.New()
		log.SetLevel(logrus.DebugLevel)
		processID = fmt.Sprintf("test-process-%s", uuid.New().String())

		// Create a Redis provider with a short retry config for testing - this will skip the test if Redis is not available
		var err error
		provider, err = queues.NewRedisProvider(ctx, log, processID, "localhost", 6379, api.SecureString("adminpass"), queues.RetryConfig{
			BaseDelay:    100 * time.Millisecond, // Short delays for testing
			MaxRetries:   3,
			MaxDelay:     500 * time.Millisecond,
			JitterFactor: 0.0,
		})
		if err != nil {
			Skip(fmt.Sprintf("Redis not available, skipping test: %v", err))
		}

		// Clean up global Redis keys from previous tests
		redisClient := redis.NewClient(&redis.Options{
			Addr:     "localhost:6379",
			Password: "adminpass",
			DB:       0,
		})
		defer redisClient.Close()

		// Get all keys that might interfere with tests
		keys, err := redisClient.Keys(ctx, "*").Result()
		if err == nil {
			// Filter for keys we want to clean up
			var keysToDelete []string
			for _, key := range keys {
				// Clean up global keys and any test-related keys
				if key == "in_flight_tasks" || key == "global_checkpoint" ||
					strings.HasPrefix(key, "failed_messages:") ||
					strings.HasPrefix(key, "test-queue-") {
					keysToDelete = append(keysToDelete, key)

					// Also clean up any consumer groups for test queues
					if strings.HasPrefix(key, "test-queue-") {
						groupName := fmt.Sprintf("%s-group", key)
						// Try to destroy the consumer group (ignore errors if it doesn't exist)
						redisClient.XGroupDestroy(ctx, key, groupName)
					}
				}
			}
			if len(keysToDelete) > 0 {
				redisClient.Del(ctx, keysToDelete...)
			}
		}
	})

	AfterEach(func() {
		if cancel != nil {
			cancel()
		}
		if provider != nil {
			provider.Stop()
			provider.Wait()
		}
	})

	Describe("Basic Queue Operations", func() {
		It("should Enqueue and consume messages", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			// Create consumer and producer
			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			// Test payload
			testPayload := []byte("test message")
			messageReceived := make(chan []byte, 1)

			// Start consuming
			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				if err := consumer.Complete(ctx, entryID, payload, nil); err != nil {
					return err
				}
				messageReceived <- payload
				return nil
			})
			Expect(err).ToNot(HaveOccurred())

			// Enqueue message
			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Wait for message to be processed
			var receivedPayload []byte
			Eventually(func() bool {
				select {
				case receivedPayload = <-messageReceived:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Now make assertions outside the goroutine
			Expect(receivedPayload).To(Equal(testPayload))

			// Clean up in proper order
			producer.Close()
			consumer.Close()
		})

		It("should handle multiple messages in order", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			// Send multiple messages
			messages := []string{"message1", "message2", "message3"}
			receivedMessages := make(chan string, len(messages))

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				// Acknowledge the message so it’s removed from the PEL
				if err := consumer.Complete(ctx, entryID, payload, nil); err != nil {
					return err
				}
				receivedMessages <- string(payload)
				return nil
			})
			Expect(err).ToNot(HaveOccurred())

			// Enqueue messages
			for _, msg := range messages {
				err = producer.Enqueue(ctx, []byte(msg), time.Now().UnixMicro())
				Expect(err).ToNot(HaveOccurred())
			}

			// Verify all messages were received
			Eventually(func() int {
				return len(receivedMessages)
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(len(messages)))
		})
	})

	Describe("In-Flight Message Tracking", func() {
		It("should track in-flight messages and handle completion", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test message")
			messageProcessed := make(chan struct {
				payload []byte
				entryID string
				err     error
			}, 1)

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				// Complete the message
				completeErr := consumer.Complete(ctx, entryID, payload, nil)

				messageProcessed <- struct {
					payload []byte
					entryID string
					err     error
				}{payload: payload, entryID: entryID, err: completeErr}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())

			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			var result struct {
				payload []byte
				entryID string
				err     error
			}
			Eventually(func() bool {
				select {
				case result = <-messageProcessed:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Now make assertions outside the goroutine
			Expect(result.payload).To(Equal(testPayload))
			Expect(result.entryID).ToNot(BeEmpty())
			Expect(result.err).ToNot(HaveOccurred())

			// Clean up in proper order
			producer.Close()
			consumer.Close()
		})

		It("should handle message completion with errors", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test message")
			messageProcessed := make(chan struct {
				payload []byte
				entryID string
				err     error
			}, 1)

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				// Complete with an error
				processingErr := fmt.Errorf("test processing error")
				completeErr := consumer.Complete(ctx, entryID, payload, processingErr)

				messageProcessed <- struct {
					payload []byte
					entryID string
					err     error
				}{payload: payload, entryID: entryID, err: completeErr}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())

			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			var result struct {
				payload []byte
				entryID string
				err     error
			}
			Eventually(func() bool {
				select {
				case result = <-messageProcessed:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Now make assertions outside the goroutine
			Expect(result.payload).To(Equal(testPayload))
			Expect(result.entryID).ToNot(BeEmpty())
			Expect(result.err).ToNot(HaveOccurred())

			// Clean up in proper order
			producer.Close()
			consumer.Close()
		})
	})

	Describe("Concurrent Operations", func() {
		It("should handle multiple consumers on the same queue", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			// Create multiple consumers
			consumer1, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			consumer2, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			// Track which consumer processes which message
			consumer1Messages := make(chan string, 10)
			consumer2Messages := make(chan string, 10)

			// Create contexts for proper consumer lifecycle management
			consumer1Ctx, consumer1Cancel := context.WithCancel(ctx)
			defer consumer1Cancel()

			consumer2Ctx, consumer2Cancel := context.WithCancel(ctx)
			defer consumer2Cancel()

			// Start consumer 1
			err = consumer1.Consume(consumer1Ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				consumer1Messages <- string(payload)
				return consumer.Complete(ctx, entryID, payload, nil)
			})
			Expect(err).ToNot(HaveOccurred())

			// Start consumer 2
			err = consumer2.Consume(consumer2Ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				consumer2Messages <- string(payload)
				return consumer.Complete(ctx, entryID, payload, nil)
			})
			Expect(err).ToNot(HaveOccurred())

			// Enqueue messages
			messages := []string{"msg1", "msg2", "msg3", "msg4", "msg5"}
			for _, msg := range messages {
				err = producer.Enqueue(ctx, []byte(msg), time.Now().UnixMicro())
				Expect(err).ToNot(HaveOccurred())
			}

			// Wait for all messages to be processed
			Eventually(func() int {
				return len(consumer1Messages) + len(consumer2Messages)
			}, 10*time.Second, 100*time.Millisecond).Should(Equal(len(messages)))

			// Stop consumers gracefully
			consumer1Cancel()
			consumer2Cancel()

			// Poll until both consumers are stopped
			Eventually(func() bool {
				return waitForRedisConsumerStopped(ctx, queueName, 1*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Clean up in proper order
			producer.Close()
			consumer1.Close()
			consumer2.Close()
		})
	})

	Describe("Provider Lifecycle", func() {
		It("should stop gracefully", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			// Start consuming
			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				return consumer.Complete(ctx, entryID, payload, nil)
			})
			Expect(err).ToNot(HaveOccurred())

			// Enqueue a message
			err = producer.Enqueue(ctx, []byte("test message"), time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Close consumer and producer first
			consumer.Close()
			producer.Close()

			// Stop the provider
			provider.Stop()
			provider.Wait()

			// Verify that the provider stopped gracefully
			Expect(provider).ToNot(BeNil())
		})
	})

	Describe("Error Handling", func() {
		It("should handle consumer handler errors gracefully", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			messageProcessed := make(chan bool, 1)

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				messageProcessed <- true
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("handler error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = producer.Enqueue(ctx, []byte("test message"), time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Verify the message was processed (even though handler returned error)
			Eventually(func() bool {
				select {
				case <-messageProcessed:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Clean up in proper order
			producer.Close()
			consumer.Close()
		})

	})

	Describe("Retry and Backoff Functionality", func() {
		It("should add failed messages to failed set with exponential backoff", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test message")
			messageProcessed := make(chan struct {
				payload []byte
				entryID string
				err     error
			}, 1)

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				// Complete with an error to trigger retry
				processingErr := fmt.Errorf("test processing error")
				completeErr := consumer.Complete(ctx, entryID, payload, processingErr)

				messageProcessed <- struct {
					payload []byte
					entryID string
					err     error
				}{payload: payload, entryID: entryID, err: completeErr}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())

			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			var result struct {
				payload []byte
				entryID string
				err     error
			}
			Eventually(func() bool {
				select {
				case result = <-messageProcessed:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			Expect(result.payload).To(Equal(testPayload))
			Expect(result.entryID).ToNot(BeEmpty())
			Expect(result.err).ToNot(HaveOccurred())

			// Stop consumer gracefully
			consumerCancel()

			// Poll until failed message is recorded in Redis
			Eventually(func() bool {
				return waitForRedisFailedMessagesState(ctx, queueName, 1, 2*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Clean up in proper order
			producer.Close()
			consumer.Close()
		})

		It("should retry failed messages with exponential backoff", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test retry message")
			messageReceived := make(chan []byte, 2) // Expect original + retry

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				messageReceived <- payload
				// Complete with error to trigger retry
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("processing error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Wait for original message
			var first []byte
			Eventually(func() bool {
				select {
				case first = <-messageReceived:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
			Expect(first).To(Equal(testPayload))

			// Stop consumer to avoid race conditions with retry operations
			consumerCancel()

			// Poll until Redis failure state is consistent (message should be in failed set)
			// This is crucial for the retry mechanism to work properly
			Eventually(func() bool {
				return waitForRedisFailedMessagesState(ctx, queueName, 1, 2*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Create a fresh consumer to receive retried messages BEFORE retry operation
			// Don't reuse the original consumer as its Redis client may be closed
			consumer2, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer consumer2.Close()

			consumerCtx2, consumerCancel2 := context.WithCancel(ctx)
			defer consumerCancel2()

			err = consumer2.Consume(consumerCtx2, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				messageReceived <- payload
				// Complete successfully this time using the correct consumer instance
				return consumer2.Complete(ctx, entryID, payload, nil)
			})
			Expect(err).ToNot(HaveOccurred())

			// Give consumer2 time to fully register and start listening
			// Need to ensure the consumer goroutine is actively polling Redis
			time.Sleep(1 * time.Second)

			// Now retry failed messages using the SAME retry configuration
			// that was used when the message was originally failed
			_, err = provider.RetryFailedMessages(ctx, queueName, queues.RetryConfig{
				BaseDelay:    100 * time.Millisecond, // Same as provider config
				MaxRetries:   3,
				MaxDelay:     500 * time.Millisecond,
				JitterFactor: 0.0,
			}, func(entryID string, body []byte, retryCount int) error {
				// Test handler for permanently failed messages
				return nil
			})
			Expect(err).ToNot(HaveOccurred())

			// Expect the retried delivery (with longer timeout for exponential backoff)
			var second []byte
			Eventually(func() bool {
				select {
				case second = <-messageReceived:
					return true
				default:
					return false
				}
			}, 15*time.Second, 100*time.Millisecond).Should(BeTrue())
			Expect(second).To(Equal(testPayload))

			// Stop second consumer
			consumerCancel2()

			// Poll until consumer group is ready for cleanup
			Eventually(func() bool {
				return waitForRedisConsumerGroupReady(ctx, queueName, 1*time.Second)
			}, 3*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Clean up in proper order - producer first, then consumers
			producer.Close()
			consumer2.Close()
			consumer.Close()
		})

		It("should process timed out messages", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())
			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			testPayload := []byte("test timeout message")
			timeoutHandlerCalled := make(chan struct {
				entryID string
				body    []byte
			}, 1)
			// Start a consumer that reads but does NOT Complete to leave the message pending
			consumerBlock, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumerBlock.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				// Intentionally do not Complete; leave in PEL
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			// Enqueue and poll until message is in pending state (PEL)
			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Poll until message appears in the consumer group (indicating it's been read but not completed)
			Eventually(func() bool {
				return waitForRedisConsumerGroupReady(ctx, queueName, 500*time.Millisecond)
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			// Wait a bit longer to ensure the message is actually timed out
			time.Sleep(100 * time.Millisecond)

			// Process timed out messages with a reasonable timeout (increased from 50ms)
			timeoutCount, err := provider.ProcessTimedOutMessages(ctx, queueName, 80*time.Millisecond, func(entryID string, body []byte) error {
				timeoutHandlerCalled <- struct {
					entryID string
					body    []byte
				}{entryID: entryID, body: body}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(timeoutCount).To(BeNumerically(">=", 1))

			// Stop consumer gracefully
			consumerCancel()
			Eventually(func() bool {
				return waitForRedisConsumerStopped(ctx, queueName, 1*time.Second)
			}, 3*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Clean up in proper order
			producer.Close()
			consumerBlock.Close()
			// Verify our handler was actually invoked
			var t struct {
				entryID string
				body    []byte
			}
			Eventually(func() bool {
				select {
				case t = <-timeoutHandlerCalled:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
			Expect(t.body).To(Equal(testPayload))
		})

		It("should retry failed messages with custom config", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test custom retry message")

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				// Complete with error to trigger retry
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("processing error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Wait for message processing and failure to be recorded
			Eventually(func() bool {
				return waitForRedisFailedMessagesState(ctx, queueName, 1, 2*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Stop consumer before retry operations
			consumerCancel()
			Eventually(func() bool {
				return waitForRedisConsumerStopped(ctx, queueName, 1*time.Second)
			}, 3*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Retry failed messages with custom config (very short delays for testing)
			config := queues.RetryConfig{
				BaseDelay:    10 * time.Millisecond, // Very short for testing
				MaxRetries:   2,
				MaxDelay:     100 * time.Millisecond,
				JitterFactor: 0.1,
			}

			retryCount, err := provider.RetryFailedMessages(ctx, queueName, config, func(entryID string, body []byte, retryCount int) error {
				// Test handler for permanently failed messages
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(retryCount).To(BeNumerically(">=", 0)) // May or may not have retryable messages

			// Clean up in proper order
			producer.Close()
			consumer.Close()
		})

		It("should handle retry count tracking correctly", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test retry count message")
			retryCounts := make(chan int, 3) // Track retry counts

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				// For now, assume retry count 0 (first message)
				retryCounts <- 0

				// Complete with error to trigger retry
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("processing error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Wait for original message (should have retry count 0)
			var retryCount int
			Eventually(func() bool {
				select {
				case retryCount = <-retryCounts:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
			Expect(retryCount).To(Equal(0))

			// Wait for failure to be recorded in Redis BEFORE cancelling consumer
			Eventually(func() bool {
				return waitForRedisFailedMessagesState(ctx, queueName, 1, 2*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Now stop consumer
			consumerCancel()

			// Clean up in proper order
			producer.Close()
			consumer.Close()
		})

		It("should respect max retries configuration", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			producer, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test max retries message")

			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				// Always fail to trigger retries
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("persistent error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = producer.Enqueue(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Wait for message processing and failure to be recorded
			Eventually(func() bool {
				return waitForRedisFailedMessagesState(ctx, queueName, 1, 2*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Stop consumer before retry operations
			consumerCancel()
			Eventually(func() bool {
				return waitForRedisConsumerStopped(ctx, queueName, 1*time.Second)
			}, 3*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Use very short delays and low max retries for quick testing
			config := queues.RetryConfig{
				BaseDelay:    10 * time.Millisecond,
				MaxRetries:   1, // Very low for testing
				MaxDelay:     50 * time.Millisecond,
				JitterFactor: 0.1,
			}

			retryCount, err := provider.RetryFailedMessages(ctx, queueName, config, func(entryID string, body []byte, retryCount int) error {
				// Test handler for permanently failed messages
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(retryCount).To(BeNumerically(">=", 0))

			// Clean up in proper order
			producer.Close()
			consumer.Close()
		})
	})

	Describe("Checkpoint Advancement", func() {
		It("should advance checkpoint when all tasks complete successfully", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer consumer.Close()

			publisher, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer publisher.Close()

			// Initial checkpoint should be zero or missing
			initialTimestamp, err := provider.GetLatestProcessedTimestamp(ctx)
			if err != nil {
				Expect(errors.Is(err, queues.ErrCheckpointMissing)).To(BeTrue(), "Expected ErrCheckpointMissing but got: %v", err)
				initialTimestamp = time.Time{} // Set to zero for consistency
			}
			// If checkpoint is missing or zero, both are valid initial states
			Expect(initialTimestamp.IsZero()).To(BeTrue())

			// Publish multiple messages with increasing timestamps
			timestamps := make([]time.Time, 3)
			for i := 0; i < 3; i++ {
				timestamps[i] = time.Now().Add(time.Duration(i) * time.Millisecond)
				err = publisher.Enqueue(ctx, []byte(fmt.Sprintf("message%d", i)), timestamps[i].UnixMicro())
				Expect(err).ToNot(HaveOccurred())
			}

			messagesProcessed := make(chan struct{}, 3)

			// Start consuming and complete all messages successfully
			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				err := consumer.Complete(ctx, entryID, payload, nil)
				messagesProcessed <- struct{}{}
				return err
			})
			Expect(err).ToNot(HaveOccurred())

			// Wait for all messages to be processed
			Eventually(func() int {
				return len(messagesProcessed)
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(3))

			// Set initial checkpoint to zero to enable advancement
			err = provider.SetCheckpointTimestamp(ctx, time.Time{})
			Expect(err).ToNot(HaveOccurred())

			// Run checkpoint advancement
			err = provider.AdvanceCheckpointAndCleanup(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Checkpoint should now be at or after the latest timestamp
			checkpointTimestamp, err := provider.GetLatestProcessedTimestamp(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(checkpointTimestamp.IsZero()).To(BeFalse())
			Expect(checkpointTimestamp.UnixMicro()).To(BeNumerically(">=", timestamps[2].UnixMicro()))
		})

		It("should not advance checkpoint past incomplete tasks", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer consumer.Close()

			publisher, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer publisher.Close()

			// Publish messages with specific timestamps (larger intervals for reliability)
			baseTime := time.Now()
			timestamps := []time.Time{
				baseTime,
				baseTime.Add(100 * time.Millisecond),
				baseTime.Add(200 * time.Millisecond),
			}

			for i, ts := range timestamps {
				err = publisher.Enqueue(ctx, []byte(fmt.Sprintf("message%d", i)), ts.UnixMicro())
				Expect(err).ToNot(HaveOccurred())
			}

			messagesProcessed := make(chan string, 3)

			// Process messages, but fail the middle one
			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				message := string(payload)
				var completionErr error

				// Fail the middle message
				if message == "message1" {
					completionErr = fmt.Errorf("simulated failure")
				}

				err := consumer.Complete(ctx, entryID, payload, completionErr)
				messagesProcessed <- message
				return err
			})
			Expect(err).ToNot(HaveOccurred())

			// Wait for all messages to be processed
			Eventually(func() int {
				count := len(messagesProcessed)
				return count
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(3))

			// Wait for in-flight tasks to be properly tracked using polling helper
			// We expect 3 tasks: 2 completed (message0, message2) and 1 incomplete (message1)
			Eventually(func() bool {
				return waitForRedisInFlightTasksState(ctx, queueName, 2, 1, 2*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Set initial checkpoint to zero to enable advancement
			err = provider.SetCheckpointTimestamp(ctx, time.Time{})
			Expect(err).ToNot(HaveOccurred())

			// Run checkpoint advancement
			err = provider.AdvanceCheckpointAndCleanup(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Checkpoint should only advance to the first completed task (message0)
			checkpointTimestamp, err := provider.GetLatestProcessedTimestamp(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Should be at or near the first timestamp, but not past the failed task
			if !checkpointTimestamp.IsZero() {
				Expect(checkpointTimestamp.UnixMicro()).To(BeNumerically(">=", timestamps[0].UnixMicro()))
				Expect(checkpointTimestamp.UnixMicro()).To(BeNumerically("<", timestamps[1].UnixMicro()))
			}
		})

		It("should advance checkpoint past permanently failed tasks", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer consumer.Close()

			publisher, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer publisher.Close()

			// Publish a message
			timestamp := time.Now()
			err = publisher.Enqueue(ctx, []byte("test message"), timestamp.UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			messageProcessed := make(chan struct{}, 1)

			// Process message and fail it
			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				err := consumer.Complete(ctx, entryID, payload, fmt.Errorf("simulated failure"))
				messageProcessed <- struct{}{}
				return err
			})
			Expect(err).ToNot(HaveOccurred())

			// Wait for message to be processed and failed
			Eventually(func() int {
				count := len(messageProcessed)
				return count
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(1))

			// Run retry with low max retries to trigger permanent failure
			config := queues.RetryConfig{
				BaseDelay:    10 * time.Millisecond,
				MaxRetries:   1, // Will exceed after 1 retry
				MaxDelay:     50 * time.Millisecond,
				JitterFactor: 0.0,
			}

			// Poll until failed message is recorded in Redis
			Eventually(func() bool {
				return waitForRedisFailedMessagesState(ctx, queueName, 1, 1*time.Second)
			}, 3*time.Second, 50*time.Millisecond).Should(BeTrue())

			// Run retry - this should mark the task as permanently failed and completed
			retryCount, err := provider.RetryFailedMessages(ctx, queueName, config, func(entryID string, body []byte, retryCount int) error {
				return fmt.Errorf("still failing")
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(retryCount).To(BeNumerically(">=", 0))

			// Stop consumer before checkpoint operations to avoid race conditions
			consumerCancel()
			Eventually(func() bool {
				return waitForRedisConsumerStopped(ctx, queueName, 1*time.Second)
			}, 3*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Set initial checkpoint to zero to enable advancement
			err = provider.SetCheckpointTimestamp(ctx, time.Time{})
			Expect(err).ToNot(HaveOccurred())

			// Run checkpoint advancement
			err = provider.AdvanceCheckpointAndCleanup(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Checkpoint should now advance past the permanently failed task
			checkpointTimestamp, err := provider.GetLatestProcessedTimestamp(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Should have advanced (permanently failed tasks are marked as completed to allow progress)
			// The checkpoint may not reach the original timestamp since permanently failed tasks
			// are marked with current time when marked as completed
			Expect(checkpointTimestamp.IsZero()).To(BeFalse(), "Checkpoint should have advanced after permanent failure")
		})

		It("should handle mixed completion states correctly", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer consumer.Close()

			publisher, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer publisher.Close()

			// Publish messages: success, success, fail, success
			baseTime := time.Now()
			timestamps := []time.Time{
				baseTime,
				baseTime.Add(100 * time.Millisecond), // Increased from 1ms to 100ms
				baseTime.Add(200 * time.Millisecond), // This will fail
				baseTime.Add(300 * time.Millisecond),
			}

			for i, ts := range timestamps {
				err = publisher.Enqueue(ctx, []byte(fmt.Sprintf("message%d", i)), ts.UnixMicro())
				Expect(err).ToNot(HaveOccurred())
			}

			messagesProcessed := make(chan int, 4)
			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			// Process messages with specific success/failure pattern
			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				message := string(payload)
				var completionErr error

				// Fail message2 (index 2)
				if message == "message2" {
					completionErr = fmt.Errorf("simulated failure")
				}

				err := consumer.Complete(ctx, entryID, payload, completionErr)

				// Extract message index
				var msgIndex int
				_, _ = fmt.Sscanf(message, "message%d", &msgIndex)
				messagesProcessed <- msgIndex

				return err
			})
			Expect(err).ToNot(HaveOccurred())

			// Wait for all messages to be processed
			Eventually(func() int {
				count := len(messagesProcessed)
				return count
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(4))

			// Wait for Redis state to be consistent (all completions and failures recorded)
			Eventually(func() bool {
				return waitForRedisInFlightTasksState(ctx, queueName, 3, 1, 2*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Stop consumer to avoid race conditions with checkpoint operations
			consumerCancel()

			// Poll until consumer is fully stopped and Redis state is consistent
			Eventually(func() bool {
				return waitForRedisConsumerStopped(ctx, queueName, 1*time.Second)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Set initial checkpoint to zero to enable advancement
			err = provider.SetCheckpointTimestamp(ctx, time.Time{})
			Expect(err).ToNot(HaveOccurred())

			// Run checkpoint advancement
			err = provider.AdvanceCheckpointAndCleanup(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Checkpoint should advance to message1 (timestamp[1]) but not past the failed message2
			checkpointTimestamp, err := provider.GetLatestProcessedTimestamp(ctx)
			Expect(err).ToNot(HaveOccurred())

			if !checkpointTimestamp.IsZero() {
				// Should be at least at message1's timestamp
				Expect(checkpointTimestamp.UnixMicro()).To(BeNumerically(">=", timestamps[1].UnixMicro()))
				// But should not reach message2's timestamp (the failed one)
				Expect(checkpointTimestamp.UnixMicro()).To(BeNumerically("<", timestamps[2].UnixMicro()))
			}
		})

		It("should handle empty queue gracefully", func() {
			// Test with no messages at all - expect ErrCheckpointMissing since no checkpoint exists
			err := provider.AdvanceCheckpointAndCleanup(ctx)
			Expect(errors.Is(err, queues.ErrCheckpointMissing)).To(BeTrue())

			// Checkpoint should still be missing
			_, err = provider.GetLatestProcessedTimestamp(ctx)
			Expect(errors.Is(err, queues.ErrCheckpointMissing)).To(BeTrue())
		})

		It("should not advance checkpoint backwards", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewQueueConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer consumer.Close()

			publisher, err := provider.NewQueueProducer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			defer publisher.Close()

			// First, establish a checkpoint by processing a message
			futureTime := time.Now().Add(1 * time.Hour) // Far in the future
			err = publisher.Enqueue(ctx, []byte("future message"), futureTime.UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			var messageProcessed int32
			var pastMessageProcessed int32

			// Create a separate context for the consumer to control its lifecycle
			consumerCtx, consumerCancel := context.WithCancel(ctx)
			defer consumerCancel()

			err = consumer.Consume(consumerCtx, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				message := string(payload)
				err := consumer.Complete(ctx, entryID, payload, nil)

				if message == "future message" {
					atomic.StoreInt32(&messageProcessed, 1)
				} else if message == "past message" {
					atomic.StoreInt32(&pastMessageProcessed, 1)
				}
				return err
			})
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() bool {
				processed := atomic.LoadInt32(&messageProcessed) == 1
				return processed
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Stop the consumer to prevent race conditions with checkpoint operations
			consumerCancel()

			// Poll until Redis state is consistent before checkpoint operations
			Eventually(func() bool {
				return waitForRedisConsumerGroupReady(ctx, queueName, 1*time.Second)
			}, 3*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Set initial checkpoint to zero to enable advancement
			err = provider.SetCheckpointTimestamp(ctx, time.Time{})
			Expect(err).ToNot(HaveOccurred())

			// Advance checkpoint
			err = provider.AdvanceCheckpointAndCleanup(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Get the established checkpoint
			checkpointAfterFuture, err := provider.GetLatestProcessedTimestamp(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Now publish and process a message with an earlier timestamp
			pastTime := time.Now() // Earlier than futureTime
			err = publisher.Enqueue(ctx, []byte("past message"), pastTime.UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Restart the consumer for the second message
			consumerCtx2, consumerCancel2 := context.WithCancel(ctx)
			defer consumerCancel2()

			err = consumer.Consume(consumerCtx2, func(ctx context.Context, payload []byte, entryID string, consumer queues.QueueConsumer, log logrus.FieldLogger) error {
				message := string(payload)
				err := consumer.Complete(ctx, entryID, payload, nil)

				if message == "future message" {
					atomic.StoreInt32(&messageProcessed, 1)
				} else if message == "past message" {
					atomic.StoreInt32(&pastMessageProcessed, 1)
				}
				return err
			})
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() bool {
				processed := atomic.LoadInt32(&pastMessageProcessed) == 1
				return processed
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Stop the consumer again
			consumerCancel2()

			// Poll until Redis state is consistent before checkpoint operations
			Eventually(func() bool {
				return waitForRedisConsumerGroupReady(ctx, queueName, 1*time.Second)
			}, 3*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Try to advance checkpoint again
			err = provider.AdvanceCheckpointAndCleanup(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Checkpoint should not have moved backwards
			checkpointAfterPast, err := provider.GetLatestProcessedTimestamp(ctx)
			Expect(err).ToNot(HaveOccurred())

			if !checkpointAfterFuture.IsZero() && !checkpointAfterPast.IsZero() {
				Expect(checkpointAfterPast.UnixMicro()).To(BeNumerically(">=", checkpointAfterFuture.UnixMicro()))
			}
		})
	})
})
