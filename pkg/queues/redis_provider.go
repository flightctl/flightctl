package queues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/config"
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
	client   *redis.Client
	log      logrus.FieldLogger
	wg       *sync.WaitGroup
	queues   []*redisQueue
	channels []*redisChannel
	stopped  atomic.Bool
	mu       sync.Mutex
}

func NewRedisProvider(ctx context.Context, log logrus.FieldLogger, hostname string, port uint, password config.SecureString) (Provider, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/queues", "RedisProvider")
	defer span.End()

	var wg sync.WaitGroup
	wg.Add(1)
	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", hostname, port),
		Password: password.Value(),
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

func (r *redisProvider) newChannel(channelName string) (*redisChannel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Load() {
		return nil, errors.New("provider is stopped")
	}
	channel := &redisChannel{
		name:   channelName,
		client: r.client,
		log:    r.log,
		wg:     r.wg,
	}
	r.channels = append(r.channels, channel)
	return channel, nil
}

func (r *redisProvider) NewQueueConsumer(queueName string) (QueueConsumer, error) {
	return r.newQueue(queueName)
}

func (r *redisProvider) NewQueueProducer(queueName string) (QueueProducer, error) {
	return r.newQueue(queueName)
}

func (r *redisProvider) NewPubSubPublisher(channelName string) (PubSubPublisher, error) {
	return r.newChannel(channelName)
}

func (r *redisProvider) NewPubSubSubscriber(channelName string) (PubSubSubscriber, error) {
	return r.newChannel(channelName)
}

func (r *redisProvider) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Swap(true) {
		return
	}
	defer r.wg.Done()

	// Close all channels
	for _, channel := range r.channels {
		channel.Close()
	}

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

func (r *redisQueue) Enqueue(ctx context.Context, payload []byte) error {
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

// redisChannel implements PubSubPublisher and PubSubSubscriber interfaces using Redis pub/sub
type redisChannel struct {
	client *redis.Client
	name   string
	log    logrus.FieldLogger
	wg     *sync.WaitGroup
	closed atomic.Bool
}

// Broadcast sends a message to all subscribers on the channel
func (r *redisChannel) Publish(ctx context.Context, payload []byte) error {
	if r.closed.Load() {
		return errors.New("channel is closed")
	}

	// Inject tracing context
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	// Create a message with context and payload
	message := map[string]interface{}{
		"body": string(payload), // Redis pub/sub works with strings
	}
	for k, v := range carrier {
		message["ctx_"+k] = v
	}

	// Convert to JSON for transmission
	messageBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal broadcast message: %w", err)
	}

	err = r.client.Publish(ctx, r.name, messageBytes).Err()
	if err != nil {
		return fmt.Errorf("failed to broadcast message: %w", err)
	}
	return nil
}

// Subscribe creates a new subscription for broadcast messages on the channel
func (r *redisChannel) Subscribe(ctx context.Context, handler PubSubHandler) (Subscription, error) {
	if r.closed.Load() {
		return nil, errors.New("channel is closed")
	}

	// Create a new pubsub connection - each subscription owns its connection
	pubsub := r.client.Subscribe(ctx, r.name)

	subscription := &redisSubscription{
		pubsub:  pubsub,
		name:    r.name,
		log:     r.log,
		wg:      r.wg,
		handler: handler,
	}

	// Start the subscription goroutine
	subscription.start(ctx)

	return subscription, nil
}

func (r *redisChannel) Close() {
	r.closed.Store(true)
}

// redisSubscription represents an active subscription that owns its pubsub connection
type redisSubscription struct {
	pubsub  *redis.PubSub
	name    string
	log     logrus.FieldLogger
	wg      *sync.WaitGroup
	handler PubSubHandler
	closed  atomic.Bool
	cancel  context.CancelFunc
}

func (s *redisSubscription) start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		defer s.pubsub.Close()

		ch := s.pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				if s.closed.Load() {
					return
				}

				if err := s.handleBroadcastMessage(ctx, msg); err != nil {
					s.log.WithError(err).Error("error while handling broadcast message")
				}
			}
		}
	}()
}

func (s *redisSubscription) handleBroadcastMessage(ctx context.Context, msg *redis.Message) error {
	ctx, parentSpan := tracing.StartSpan(ctx, "flightctl/queues/broadcast", s.name)
	defer parentSpan.End()

	// Parse the JSON message
	var message map[string]interface{}
	if err := json.Unmarshal([]byte(msg.Payload), &message); err != nil {
		parentSpan.RecordError(err)
		parentSpan.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("failed to unmarshal broadcast message: %w", err)
	}

	// Extract tracing context
	carrier := propagation.MapCarrier{}
	for k, v := range message {
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
		receivedCtx, "flightctl/queues/broadcast", s.name, trace.WithLinks(
			trace.LinkFromContext(ctx, attribute.String("request.id", requestID))))
	defer handlerSpan.End()

	handlerSpan.SetAttributes(attribute.String("request.id", requestID))
	parentSpan.SetAttributes(attribute.String("request.id", requestID))

	receivedCtx = context.WithValue(receivedCtx, middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(receivedCtx, s.log)

	// Extract the payload
	var body []byte
	if bodyStr, ok := message["body"].(string); ok {
		body = []byte(bodyStr)
	} else {
		err := fmt.Errorf("unexpected body type %T in broadcast message", message["body"])
		handlerSpan.RecordError(err)
		handlerSpan.SetStatus(codes.Error, "unexpected body type")
		return err
	}

	// Run handler
	if err := s.handler(receivedCtx, body, log); err != nil {
		handlerSpan.RecordError(err)
		handlerSpan.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("broadcast handler error: %w", err)
	}

	return nil
}

func (s *redisSubscription) Close() {
	if s.closed.Swap(true) {
		return
	}

	if s.cancel != nil {
		s.cancel()
	}
}
