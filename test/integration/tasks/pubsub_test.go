package tasks_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/pkg/queues"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("PubSub Integration Tests", func() {
	var (
		log      *logrus.Logger
		ctx      context.Context
		cancel   context.CancelFunc
		provider queues.Provider
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		log = logrus.New()

		var err error
		provider, err = queues.NewRedisProvider(ctx, log, fmt.Sprintf("test-pubsub-%d", GinkgoParallelProcess()), redisHost, redisPort, redisPassword, queues.RetryConfig{
			BaseDelay:    100 * time.Millisecond,
			MaxRetries:   3,
			MaxDelay:     500 * time.Millisecond,
			JitterFactor: 0.0,
		})
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if provider != nil {
			provider.Stop()
			provider.Wait()
		}
		cancel()
	})

	Describe("Publish and Subscribe", func() {
		It("When ... it should deliver a message to multiple subscribers", func() {
			channel := fmt.Sprintf("test-pubsub-channel-%d", GinkgoParallelProcess())
			payload := []byte("hello subscribers")

			const numSubscribers = 3
			received := make([][]byte, numSubscribers)
			var mu sync.Mutex

			for i := 0; i < numSubscribers; i++ {
				idx := i
				subscriber, err := provider.NewPubSubSubscriber(ctx, channel)
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(subscriber.Close)

				sub, err := subscriber.Subscribe(ctx, func(_ context.Context, p []byte, _ logrus.FieldLogger) error {
					mu.Lock()
					received[idx] = p
					mu.Unlock()
					return nil
				})
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(sub.Close)
			}

			// Give subscribers time to register
			time.Sleep(100 * time.Millisecond)

			publisher, err := provider.NewPubSubPublisher(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(publisher.Close)

			Expect(publisher.Publish(ctx, payload)).To(Succeed())

			Eventually(func() bool {
				mu.Lock()
				defer mu.Unlock()
				for _, r := range received {
					if r == nil {
						return false
					}
				}
				return true
			}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

			mu.Lock()
			defer mu.Unlock()
			for i, r := range received {
				Expect(r).To(Equal(payload), "subscriber %d should have received the message", i)
			}
		})

		It("When ... it should not deliver old messages to a late subscriber", func() {
			channel := fmt.Sprintf("test-pubsub-late-%d", GinkgoParallelProcess())

			publisher, err := provider.NewPubSubPublisher(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(publisher.Close)

			Expect(publisher.Publish(ctx, []byte("early message"))).To(Succeed())
			time.Sleep(100 * time.Millisecond)

			// Subscribe after the message was published
			subscriber, err := provider.NewPubSubSubscriber(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(subscriber.Close)

			var received []byte
			subCtx, subCancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer subCancel()

			sub, err := subscriber.Subscribe(subCtx, func(_ context.Context, p []byte, _ logrus.FieldLogger) error {
				received = p
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(sub.Close)

			// Wait for the subscription context to expire
			<-subCtx.Done()

			Expect(received).To(BeNil(), "late subscriber should not receive messages published before it subscribed")
		})

		It("When ... it should handle a handler error without hanging", func() {
			channel := fmt.Sprintf("test-pubsub-error-%d", GinkgoParallelProcess())

			subscriber, err := provider.NewPubSubSubscriber(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(subscriber.Close)

			sub, err := subscriber.Subscribe(ctx, func(_ context.Context, _ []byte, _ logrus.FieldLogger) error {
				return fmt.Errorf("handler error")
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(sub.Close)

			time.Sleep(100 * time.Millisecond)

			publisher, err := provider.NewPubSubPublisher(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(publisher.Close)

			// Should not hang even though the handler returns an error
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = publisher.Publish(ctx, []byte("trigger error"))
			}()
			Eventually(done, 3*time.Second).Should(BeClosed())
		})

		It("When ... it should return an error when publishing on a closed publisher", func() {
			channel := fmt.Sprintf("test-pubsub-closed-%d", GinkgoParallelProcess())

			publisher, err := provider.NewPubSubPublisher(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			publisher.Close()

			err = publisher.Publish(ctx, []byte("should fail"))
			Expect(err).To(HaveOccurred())
		})

		It("When ... it should return an error when subscribing on a closed subscriber", func() {
			channel := fmt.Sprintf("test-pubsub-closed-sub-%d", GinkgoParallelProcess())

			subscriber, err := provider.NewPubSubSubscriber(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			subscriber.Close()

			_, err = subscriber.Subscribe(ctx, func(_ context.Context, _ []byte, _ logrus.FieldLogger) error {
				return nil
			})
			Expect(err).To(HaveOccurred())
		})

		It("When ... it should support multiple subscriptions from the same subscriber", func() {
			channel := fmt.Sprintf("test-pubsub-multi-sub-%d", GinkgoParallelProcess())
			payload := []byte("test message")

			subscriber, err := provider.NewPubSubSubscriber(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(subscriber.Close)

			var count1, count2 int
			var mu sync.Mutex

			sub1, err := subscriber.Subscribe(ctx, func(_ context.Context, _ []byte, _ logrus.FieldLogger) error {
				mu.Lock()
				count1++
				mu.Unlock()
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(sub1.Close)

			sub2, err := subscriber.Subscribe(ctx, func(_ context.Context, _ []byte, _ logrus.FieldLogger) error {
				mu.Lock()
				count2++
				mu.Unlock()
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(sub2.Close)

			time.Sleep(100 * time.Millisecond)

			publisher, err := provider.NewPubSubPublisher(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(publisher.Close)

			Expect(publisher.Publish(ctx, payload)).To(Succeed())

			Eventually(func() bool {
				mu.Lock()
				defer mu.Unlock()
				return count1 >= 1 && count2 >= 1
			}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())
		})
	})
})
