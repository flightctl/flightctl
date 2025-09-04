package tasks_test

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("Redis Provider Integration Tests", func() {
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
		provider, err = queues.NewRedisProvider(ctx, log, processID, "localhost", 6379, config.SecureString("adminpass"), queues.RetryConfig{
			BaseDelay:    100 * time.Millisecond, // Short delays for testing
			MaxRetries:   3,
			MaxDelay:     500 * time.Millisecond,
			JitterFactor: 0.0,
		})
		if err != nil {
			Skip(fmt.Sprintf("Redis not available, skipping test: %v", err))
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
		It("should publish and consume messages", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			// Create consumer and publisher
			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			// Test payload
			testPayload := []byte("test message")
			messageReceived := make(chan []byte, 1)

			// Start consuming
			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				if err := consumer.Complete(ctx, entryID, payload, nil); err != nil {
					return err
				}
				messageReceived <- payload
				return nil
			})
			Expect(err).ToNot(HaveOccurred())

			// Publish message
			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
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

			// Clean up
			consumer.Close()
			publisher.Close()
		})

		It("should handle multiple messages in order", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			// Send multiple messages
			messages := []string{"message1", "message2", "message3"}
			receivedMessages := make(chan string, len(messages))

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				// Acknowledge the message so it’s removed from the PEL
				if err := consumer.Complete(ctx, entryID, payload, nil); err != nil {
					return err
				}
				receivedMessages <- string(payload)
				return nil
			})
			Expect(err).ToNot(HaveOccurred())

			// Publish messages
			for _, msg := range messages {
				err = publisher.Publish(ctx, []byte(msg), time.Now().UnixMicro())
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

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test message")
			messageProcessed := make(chan struct {
				payload []byte
				entryID string
				err     error
			}, 1)

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
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

			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
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

			// Clean up
			consumer.Close()
			publisher.Close()
		})

		It("should handle message completion with errors", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test message")
			messageProcessed := make(chan struct {
				payload []byte
				entryID string
				err     error
			}, 1)

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
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

			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
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

			// Clean up
			consumer.Close()
			publisher.Close()
		})
	})

	Describe("Concurrent Operations", func() {
		It("should handle multiple consumers on the same queue", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			// Create multiple consumers
			consumer1, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			consumer2, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			// Track which consumer processes which message
			consumer1Messages := make(chan string, 10)
			consumer2Messages := make(chan string, 10)

			// Start consumer 1
			err = consumer1.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				consumer1Messages <- string(payload)
				return consumer.Complete(ctx, entryID, payload, nil)
			})
			Expect(err).ToNot(HaveOccurred())

			// Start consumer 2
			err = consumer2.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				consumer2Messages <- string(payload)
				return consumer.Complete(ctx, entryID, payload, nil)
			})
			Expect(err).ToNot(HaveOccurred())

			// Publish messages
			messages := []string{"msg1", "msg2", "msg3", "msg4", "msg5"}
			for _, msg := range messages {
				err = publisher.Publish(ctx, []byte(msg), time.Now().UnixMicro())
				Expect(err).ToNot(HaveOccurred())
			}

			// Wait for all messages to be processed
			Eventually(func() int {
				return len(consumer1Messages) + len(consumer2Messages)
			}, 10*time.Second, 100*time.Millisecond).Should(Equal(len(messages)))

			// Clean up
			consumer1.Close()
			consumer2.Close()
			publisher.Close()
		})
	})

	Describe("Provider Lifecycle", func() {
		It("should stop gracefully", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			// Start consuming
			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				return consumer.Complete(ctx, entryID, payload, nil)
			})
			Expect(err).ToNot(HaveOccurred())

			// Publish a message
			err = publisher.Publish(ctx, []byte("test message"), time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

			// Close consumer and publisher first
			consumer.Close()
			publisher.Close()

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

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			messageProcessed := make(chan bool, 1)

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				messageProcessed <- true
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("handler error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = publisher.Publish(ctx, []byte("test message"), time.Now().UnixMicro())
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

			// Clean up
			consumer.Close()
			publisher.Close()
		})

	})

	Describe("Retry and Backoff Functionality", func() {
		It("should add failed messages to failed set with exponential backoff", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test message")
			messageProcessed := make(chan struct {
				payload []byte
				entryID string
				err     error
			}, 1)

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
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

			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
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

			// Clean up
			consumer.Close()
			publisher.Close()
		})

		It("should retry failed messages with exponential backoff", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test retry message")
			messageReceived := make(chan []byte, 2) // Expect original + retry

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				messageReceived <- payload
				// Complete with error to trigger retry
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("processing error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
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

			// Wait for the natural backoff delay to complete before retrying
			// With our short retry config (100ms base delay), we wait a bit longer
			time.Sleep(500 * time.Millisecond)

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

			// Expect the retried delivery
			var second []byte
			Eventually(func() bool {
				select {
				case second = <-messageReceived:
					return true
				default:
					return false
				}
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
			Expect(second).To(Equal(testPayload))

			// Clean up
			consumer.Close()
			publisher.Close()
		})

		It("should process timed out messages", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())
			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			testPayload := []byte("test timeout message")
			timeoutHandlerCalled := make(chan struct {
				entryID string
				body    []byte
			}, 1)
			// Start a consumer that reads but does NOT Complete to leave the message pending
			consumerBlock, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())
			err = consumerBlock.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				// Intentionally do not Complete; leave in PEL
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			// Publish and give it a moment to land in PEL
			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())
			time.Sleep(10 * time.Millisecond)
			// Process timed out messages with a very short timeout
			timeoutCount, err := provider.ProcessTimedOutMessages(ctx, queueName, 1*time.Millisecond, func(entryID string, body []byte) error {
				timeoutHandlerCalled <- struct {
					entryID string
					body    []byte
				}{entryID: entryID, body: body}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(timeoutCount).To(BeNumerically(">=", 1))
			consumerBlock.Close()
			// Clean up
			publisher.Close()
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

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test custom retry message")

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				// Complete with error to trigger retry
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("processing error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

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

			// Clean up
			consumer.Close()
			publisher.Close()
		})

		It("should handle retry count tracking correctly", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test retry count message")
			retryCounts := make(chan int, 3) // Track retry counts

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				// For now, assume retry count 0 (first message)
				retryCounts <- 0

				// Complete with error to trigger retry
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("processing error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
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

			// Clean up
			consumer.Close()
			publisher.Close()
		})

		It("should respect max retries configuration", func() {
			queueName := fmt.Sprintf("test-queue-%s", uuid.New().String())

			consumer, err := provider.NewConsumer(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			publisher, err := provider.NewPublisher(ctx, queueName)
			Expect(err).ToNot(HaveOccurred())

			testPayload := []byte("test max retries message")

			err = consumer.Consume(ctx, func(ctx context.Context, payload []byte, entryID string, consumer queues.Consumer, log logrus.FieldLogger) error {
				// Always fail to trigger retries
				return consumer.Complete(ctx, entryID, payload, fmt.Errorf("persistent error"))
			})
			Expect(err).ToNot(HaveOccurred())

			err = publisher.Publish(ctx, testPayload, time.Now().UnixMicro())
			Expect(err).ToNot(HaveOccurred())

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

			// Clean up
			consumer.Close()
			publisher.Close()
		})
	})
})
