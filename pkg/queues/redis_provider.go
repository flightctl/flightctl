package queues

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type redisProvider struct {
	client  *redis.Client
	log     logrus.FieldLogger
	wg      *sync.WaitGroup
	queues  []*redisQueue
	stopped atomic.Bool
	mu      sync.Mutex
}

func NewRedisProvider(ctx context.Context, log logrus.FieldLogger, hostname string, port uint, password string) (Provider, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/queues", "RedisProvider")
	defer span.End()

	var wg sync.WaitGroup
	wg.Add(1)
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", hostname, port),
		Password: password,
		DB:       0,
	})

	// Enable tracing instrumentation.
	if err := redisotel.InstrumentTracing(client); err != nil {
		return nil, fmt.Errorf("failed to enable Redis tracing instrumentation: %w", err)
	}

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
	return r.newQueue(queueName)
}

func (r *redisProvider) NewPublisher(queueName string) (Publisher, error) {
	return r.newQueue(queueName)
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

func (r *redisQueue) Publish(ctx context.Context, payload []byte) error {
	if r.closed.Load() {
		return errors.New("queue is closed")
	}

	// Inject tracing context
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	// Store the context + payload in the message
	values := map[string]interface{}{
		"body": payload,
	}
	for k, v := range carrier {
		values["ctx_"+k] = v
	}

	_, err := r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: r.name,
		Values: values,
	}).Result()
	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}
	return nil
}

func (r *redisQueue) Consume(ctx context.Context, handler ConsumeHandler) error {
	ctx, cancel := context.WithCancel(ctx)
	r.wg.Add(1)

	go func() {
		defer r.wg.Done()
		defer cancel()

		for {
			if r.closed.Load() {
				return
			}

			if err := r.consumeOnce(ctx, handler); err != nil {
				r.log.WithError(err).Error("error while consuming message")
			}
		}
	}()

	return nil
}

func (r *redisQueue) consumeOnce(ctx context.Context, handler ConsumeHandler) error {
	ctx, parentSpan := tracing.StartSpan(ctx, "flightctl/queues", r.name)
	defer parentSpan.End()

	msgs, err := r.client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{r.name, "0"},
		Count:   1,
		Block:   0,
	}).Result()
	if err != nil {
		parentSpan.RecordError(err)
		parentSpan.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("failed to read from stream: %w", err)
	}

	var handlerErrs []error

	for _, msg := range msgs {
		for _, entry := range msg.Messages {
			// Extract tracing context from Redis stream entry
			carrier := propagation.MapCarrier{}
			for k, v := range entry.Values {
				if len(k) > 4 && k[:4] == "ctx_" {
					if valStr, ok := v.(string); ok {
						carrier[k[4:]] = valStr
					}
				}
			}

			receivedCtx := otel.GetTextMapPropagator().Extract(ctx, carrier)
			requestID := reqid.NextRequestID()

			// Start span for handler logic
			receivedCtx, handlerSpan := tracing.StartSpan(
				receivedCtx, "flightctl/queues", r.name, trace.WithLinks(
					trace.LinkFromContext(ctx, attribute.String("request.id", requestID))))

			handlerSpan.SetAttributes(attribute.String("request.id", requestID))
			parentSpan.SetAttributes(attribute.String("request.id", requestID))

			receivedCtx = context.WithValue(receivedCtx, middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(receivedCtx, r.log)

			// Safe type conversion for body
			var body []byte
			switch v := entry.Values["body"].(type) {
			case []byte:
				body = v
			case string:
				body = []byte(v)
			default:
				err := fmt.Errorf("unexpected body type %T", v)
				handlerSpan.RecordError(err)
				handlerSpan.SetStatus(codes.Error, "unexpected body type")

				// Attempt to delete the bad message
				if _, delErr := r.client.XDel(ctx, r.name, entry.ID).Result(); delErr != nil {
					parentSpan.RecordError(delErr)
					parentSpan.SetStatus(codes.Error, delErr.Error())
					handlerSpan.End()
					return fmt.Errorf("failed to delete message ID %s after body type error: %w", entry.ID, delErr)
				}

				handlerSpan.End()
				handlerErrs = append(handlerErrs, err)
				continue
			}

			// Run handler and collect error if occurs
			if err := handler(receivedCtx, body, log); err != nil {
				handlerSpan.RecordError(err)
				handlerSpan.SetStatus(codes.Error, err.Error())
				handlerErrs = append(handlerErrs, fmt.Errorf("handler error on ID %s: %w", entry.ID, err))
			}

			// Always attempt to delete the message
			if _, err := r.client.XDel(ctx, r.name, entry.ID).Result(); err != nil {
				parentSpan.RecordError(err)
				parentSpan.SetStatus(codes.Error, err.Error())
				handlerSpan.End()
				return fmt.Errorf("failed to delete message ID %s: %w", entry.ID, err)
			}

			handlerSpan.End()
		}
	}

	// After processing all entries, report handler errors if any
	if len(handlerErrs) > 0 {
		return fmt.Errorf("one or more handler errors occurred: %v", handlerErrs)
	}

	return nil
}

func (r *redisQueue) Close() {
	if r.closed.Swap(true) {
		return
	}
}
