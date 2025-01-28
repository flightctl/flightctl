package queues

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

const QueuePrefix = "flightctl"

type redisProvider struct {
	client  *redis.Client
	log     logrus.FieldLogger
	wg      *sync.WaitGroup
	queues  []*redisQueue
	stopped atomic.Bool
	mu      sync.Mutex
}

func NewRedisProvider(ctx context.Context, log logrus.FieldLogger, hostname string, port uint, password string) (Provider, error) {
	var wg sync.WaitGroup
	wg.Add(1)
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", hostname, port),
		Password: password,
		DB:       0,
	})

	// Test the connection
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := client.Ping(timeoutCtx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis queue: %w", err)
	}
	log.Info("successfully connected to the Redis queue")

	return &redisProvider{
		client: client,
		log:    log,
		wg:     &wg,
	}, nil
}

func (r *redisProvider) newQueue(queueName string) (*redisQueue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Load() {
		return nil, errors.New("provider is stopped")
	}
	queue := &redisQueue{
		name:   queueName,
		client: r.client,
		log:    r.log,
		wg:     r.wg,
	}
	r.queues = append(r.queues, queue)
	return queue, nil
}

func (r *redisProvider) NewConsumer(queueName string) (Consumer, error) {
	prefixedName := fmt.Sprintf("%s:%s", QueuePrefix, queueName)
	return r.newQueue(prefixedName)
}

func (r *redisProvider) NewPublisher(queueName string) (Publisher, error) {
	prefixedName := fmt.Sprintf("%s:%s", QueuePrefix, queueName)
	return r.newQueue(prefixedName)
}

func (r *redisProvider) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Swap(true) {
		return
	}
	defer r.wg.Done()
	r.client.Close()
}

func (r *redisProvider) Wait() {
	r.wg.Wait()
}

type redisQueue struct {
	client *redis.Client
	name   string
	log    logrus.FieldLogger
	wg     *sync.WaitGroup
	closed atomic.Bool
}

func (r *redisQueue) Publish(payload []byte) error {
	if r.closed.Load() {
		return errors.New("queue is closed")
	}
	_, err := r.client.XAdd(context.Background(), &redis.XAddArgs{
		Stream: r.name,
		Values: map[string]interface{}{"body": payload},
	}).Result()
	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}
	return nil
}

func (r *redisQueue) Consume(ctx context.Context, handler ConsumeHandler) error {
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		for {
			if r.closed.Load() {
				return
			}
			msgs, err := r.client.XRead(ctx, &redis.XReadArgs{
				Streams: []string{r.name, "0"},
				Count:   1,
				Block:   0,
			}).Result()
			if err != nil {
				r.log.WithError(err).Error("failed to read from stream")
				continue
			}
			for _, msg := range msgs {
				for _, entry := range msg.Messages {
					body, ok := entry.Values["body"].([]byte)
					if !ok {
						body = []byte(fmt.Sprint(entry.Values["body"]))
					}
					requestID := reqid.NextRequestID()
					reqCtx := context.WithValue(ctx, middleware.RequestIDKey, requestID)
					log := log.WithReqIDFromCtx(reqCtx, r.log)
					if err := handler(reqCtx, body, log); err != nil {
						log.WithError(err).Errorf("failed to consume message: %s", string(body))
					}
					_, err := r.client.XDel(ctx, r.name, entry.ID).Result()
					if err != nil {
						log.WithError(err).Errorf("failed to delete message")
					}
				}
			}
		}
	}()
	return nil
}

func (r *redisQueue) Close() {
	if r.closed.Swap(true) {
		return
	}
}
