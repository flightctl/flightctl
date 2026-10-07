package tasks_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/pkg/queues"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

var _ = Describe("PubSub Integration Tests", func() {
	var (
		log      *logrus.Logger
		ctx      context.Context
		cancel   context.CancelFunc
		provider queues.Provider
		client   *redis.Client
	)

	BeforeEach(func() {
		provider = nil
		client = nil
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
		client = redis.NewClient(&redis.Options{
			Addr:     fmt.Sprintf("%s:%d", redisHost, redisPort),
			Password: string(redisPassword),
		})
	})

	AfterEach(func() {
		if cancel != nil {
			cancel()
		}
		if provider != nil {
			provider.Stop()
			provider.Wait()
		}
		if client != nil {
			Expect(client.Close()).To(Succeed())
		}
	})

	waitForSubscribers := func(channel string, count int64) {
		GinkgoHelper()
		Eventually(func() (int64, error) {
			counts, err := client.PubSubNumSub(ctx, channel).Result()
			return counts[channel], err
		}, 5*time.Second, 50*time.Millisecond).Should(Equal(count))
	}

	Describe("Publish and Subscribe", func() {
		It("When a message is published it should deliver to multiple subscribers", func() {
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

			waitForSubscribers(channel, numSubscribers)

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

		It("When a subscriber joins after publish it should not receive old messages", func() {
			channel := fmt.Sprintf("test-pubsub-late-%d", GinkgoParallelProcess())

			publisher, err := provider.NewPubSubPublisher(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(publisher.Close)

			Expect(publisher.Publish(ctx, []byte("early message"))).To(Succeed())

			// Subscribe after the message was published
			subscriber, err := provider.NewPubSubSubscriber(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(subscriber.Close)

			received := make(chan []byte, 1)

			sub, err := subscriber.Subscribe(ctx, func(_ context.Context, payload []byte, _ logrus.FieldLogger) error {
				received <- payload
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(sub.Close)

			waitForSubscribers(channel, 1)
			Consistently(received, 500*time.Millisecond).ShouldNot(Receive(), "late subscriber should not receive messages published before it subscribed")
		})

		It("When a handler returns an error it should not hang the publisher", SpecTimeout(30*time.Second), func(specCtx SpecContext) {
			channel := fmt.Sprintf("test-pubsub-error-%d", GinkgoParallelProcess())

			subscriber, err := provider.NewPubSubSubscriber(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(subscriber.Close)

			handled := make(chan struct{}, 1)
			sub, err := subscriber.Subscribe(ctx, func(_ context.Context, _ []byte, _ logrus.FieldLogger) error {
				handled <- struct{}{}
				return fmt.Errorf("handler error")
			})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(sub.Close)

			waitForSubscribers(channel, 1)

			publisher, err := provider.NewPubSubPublisher(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(publisher.Close)

			publishCtx, publishCancel := context.WithTimeout(specCtx, 3*time.Second)
			defer publishCancel()
			Expect(publisher.Publish(publishCtx, []byte("trigger error"))).To(Succeed())
			Eventually(handled, 3*time.Second).Should(Receive())
		})

		It("When the publisher is closed it should return an error on publish", func() {
			channel := fmt.Sprintf("test-pubsub-closed-%d", GinkgoParallelProcess())

			publisher, err := provider.NewPubSubPublisher(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			publisher.Close()

			err = publisher.Publish(ctx, []byte("should fail"))
			Expect(err).To(HaveOccurred())
		})

		It("When the subscriber is closed it should return an error on subscribe", func() {
			channel := fmt.Sprintf("test-pubsub-closed-sub-%d", GinkgoParallelProcess())

			subscriber, err := provider.NewPubSubSubscriber(ctx, channel)
			Expect(err).ToNot(HaveOccurred())
			subscriber.Close()

			_, err = subscriber.Subscribe(ctx, func(_ context.Context, _ []byte, _ logrus.FieldLogger) error {
				return nil
			})
			Expect(err).To(HaveOccurred())
		})

		It("When a subscriber registers multiple handlers it should deliver to all handlers", func() {
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

			waitForSubscribers(channel, 2)

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
