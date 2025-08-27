package queues

/*
Redis Provider Implementation - Native Stream Consumer Groups

This provider implements a reliable message queue system using Redis Streams with native
consumer groups for automatic acknowledgment and pending message tracking.

Data Structures:
1. Redis Stream: <queue_name>
   - Stores incoming messages with tracing context
   - Uses consumer groups for automatic message tracking

2. Consumer Group: <queue_name>:<group_name>
   - Automatically tracks pending messages
   - Provides XPENDING for timeout detection
   - Messages are deleted immediately after processing

3. Failed Messages Sorted Set: failed_messages:<queue_name>
   - Member: <entryID>|base64(<body>)|<processID>|<retryCount>
   - Score: retry timestamp (when message should be retried)
   - Stores failed messages with exponential backoff retry scheduling
   - Sorted by retry timestamp for efficient retry processing
   - processID included for audit purposes
   - retryCount tracks number of retry attempts for exponential backoff
   - body is base64 encoded to handle any characters including pipe characters

Message Flow:
1. Message published to Redis stream with tracing context and timestamp
2. Consumer reads message using XREADGROUP
3. Handler processes message
4. Message completed:
   - If failed: added to failed messages sorted set
   - Always: XDEL to remove from stream

Crash Recovery:
- Pending messages automatically tracked by Redis consumer groups
- Failed messages preserved in sorted set for retry
- Process ID ensures worker isolation

Performance Characteristics:
- O(1) for message deletion
- O(1) for pending message detection via XPENDING
- Native Redis Streams consumer group functionality
- No custom in-flight tracking needed
- Exponential backoff with jitter prevents thundering herd during outages
- Configurable retry limits and backoff parameters
*/
import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
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
	client      *redis.Client
	log         logrus.FieldLogger
	wg          *sync.WaitGroup
	queues      []*redisQueue
	stopped     atomic.Bool
	mu          sync.Mutex
	processID   string
	retryConfig RetryConfig
}

func NewRedisProvider(ctx context.Context, log logrus.FieldLogger, processID string, hostname string, port uint, password config.SecureString, retryConfig RetryConfig) (Provider, error) {
	if processID == "" {
		return nil, errors.New("processID cannot be empty")
	}
	if strings.Contains(processID, "|") {
		return nil, errors.New("processID cannot contain pipe character")
	}

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
		client:      client,
		log:         log,
		wg:          &wg,
		processID:   processID,
		retryConfig: retryConfig,
	}, nil
}

// findExistingQueue finds an existing queue by name, thread-safe
func (r *redisProvider) findExistingQueue(queueName string) *redisQueue {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.queues {
		if q.name == queueName && !q.closed.Load() {
			return q
		}
	}
	return nil
}

func (r *redisProvider) newQueue(ctx context.Context, queueName string) (*redisQueue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Load() {
		return nil, errors.New("provider is stopped")
	}

	// Deduplicate: return existing active queue if present.
	for _, q := range r.queues {
		if q.name == queueName && !q.closed.Load() {
			return q, nil
		}
	}

	// Create consumer group name and consumer name
	groupName := fmt.Sprintf("%s-group", queueName)
	consumerName := fmt.Sprintf("%s-consumer-%s", queueName, r.processID)

	// Ensure stream+group exist with passed context.
	if err := r.client.XGroupCreateMkStream(ctx, queueName, groupName, "0").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, fmt.Errorf("failed to create consumer group: %w", err)
	}

	queue := &redisQueue{
		name:         queueName,
		client:       r.client,
		groupName:    groupName,
		consumerName: consumerName,
		log:          r.log,
		wg:           r.wg,
		processID:    r.processID,
		retryConfig:  r.retryConfig,
	}
	r.queues = append(r.queues, queue)
	return queue, nil
}

func (r *redisProvider) NewConsumer(ctx context.Context, queueName string) (Consumer, error) {
	return r.newQueue(ctx, queueName)
}

func (r *redisProvider) NewPublisher(ctx context.Context, queueName string) (Publisher, error) {
	return r.newQueue(ctx, queueName)
}

func (r *redisProvider) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Swap(true) {
		return
	}
	// Signal all queue goroutines to exit.
	for _, q := range r.queues {
		q.Close()
	}
	defer r.wg.Done()
	// Break any blocking reads.
	_ = r.client.Close()
}

func (r *redisProvider) Wait() {
	r.wg.Wait()
}

func (r *redisProvider) CheckHealth(ctx context.Context) error {
	if r.client == nil {
		return errors.New("redis client not initialized")
	}
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

func (r *redisProvider) ProcessTimedOutMessages(ctx context.Context, queueName string, timeout time.Duration, handler func(entryID, body string) error) (int, error) {
	// Find existing queue or create a temporary one
	queue := r.findExistingQueue(queueName)

	if queue == nil {
		var err error
		queue, err = r.newQueue(ctx, queueName)
		if err != nil {
			return 0, err
		}
	}
	return queue.ProcessTimedOutMessages(ctx, timeout, handler)
}

func (r *redisProvider) RetryFailedMessages(ctx context.Context, queueName string, config RetryConfig) (int, error) {
	// Find existing queue or create a temporary one
	queue := r.findExistingQueue(queueName)

	if queue == nil {
		var err error
		queue, err = r.newQueue(ctx, queueName)
		if err != nil {
			return 0, err
		}
	}
	return queue.RetryFailedMessages(ctx, config)
}

type redisQueue struct {
	client       *redis.Client
	name         string
	groupName    string
	consumerName string
	log          logrus.FieldLogger
	wg           *sync.WaitGroup
	processID    string
	closed       atomic.Bool
	retryConfig  RetryConfig
}

// Helper functions for time bucket operations

// RetryConfig holds configuration for exponential backoff retry logic
type RetryConfig struct {
	BaseDelay    time.Duration // Base delay for exponential backoff
	MaxRetries   int           // Maximum number of retry attempts
	MaxDelay     time.Duration // Maximum delay cap
	JitterFactor float64       // Jitter factor (0.0 to 1.0)
}

// DefaultRetryConfig returns a sensible default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		BaseDelay:    1 * time.Second,
		MaxRetries:   5,
		MaxDelay:     1 * time.Hour,
		JitterFactor: 0.1, // 10% jitter
	}
}

// calculateBackoff calculates the backoff delay for a given retry count
func calculateBackoff(retryCount int, config RetryConfig) time.Duration {
	// Calculate exponential delay: baseDelay * (2^retryCount)
	delay := config.BaseDelay * time.Duration(1<<retryCount)

	// Cap at maximum delay
	if delay > config.MaxDelay {
		delay = config.MaxDelay
	}

	// Add jitter to prevent thundering herd
	if config.JitterFactor > 0 {
		jitterRange := float64(delay) * config.JitterFactor
		jitter := time.Duration((rand.Float64()*2 - 1) * jitterRange) //nolint:gosec
		delay += jitter
	}
	if delay < 0 {
		delay = 0
	}

	return delay
}

// formatFailedMember formats a failed member with retry count.
// Format: "entryID|base64(body)|processID|retryCount"
// Input: entryID (string), body (bytes), retryCount (int)
// Returns: formatted string
func (r *redisQueue) formatFailedMember(entryID string, body []byte, retryCount int) string {
	// Base64 encode the body to handle any characters including pipe characters
	encodedBody := base64.StdEncoding.EncodeToString(body)
	return fmt.Sprintf("%s|%s|%s|%d", entryID, encodedBody, r.processID, retryCount)
}

// parseFailedMember parses a failed member with retry count.
// Format: "entryID|base64(body)|processID|retryCount"
// Returns: entryID (string), body (bytes), processID (string), retryCount (int), error
func (r *redisQueue) parseFailedMember(failedMember string) (string, []byte, string, int, error) {
	parts := strings.SplitN(failedMember, "|", 4)
	if len(parts) != 4 {
		return "", nil, "", 0, fmt.Errorf("invalid failed member format: %s", failedMember)
	}

	retryCount, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", nil, "", 0, fmt.Errorf("invalid retry count in failed member: %s", failedMember)
	}

	// Decode the base64 body
	decodedBody, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", nil, "", 0, fmt.Errorf("failed to decode base64 body in failed member: %s", failedMember)
	}

	return parts[0], decodedBody, parts[2], retryCount, nil // entryID, body, processID, retryCount
}

func (r *redisQueue) Publish(ctx context.Context, payload []byte, timestamp int64) error {
	if r.closed.Load() {
		return errors.New("queue is closed")
	}

	// Inject tracing context
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	// Store the context + payload + timestamp in the message
	values := map[string]interface{}{
		"body":      payload,
		"timestamp": timestamp,
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
	r.wg.Add(1)

	go func() {
		defer r.wg.Done()

		for {
			select {
			case <-ctx.Done():
				return
			default:
				if r.closed.Load() {
					return
				}

				if err := r.consumeOnce(ctx, handler); err != nil {
					r.log.WithError(err).Error("error while consuming message")
				}
			}
		}
	}()

	return nil
}

func (r *redisQueue) consumeOnce(ctx context.Context, handler ConsumeHandler) error {
	ctx, parentSpan := tracing.StartSpan(ctx, "flightctl/queues", r.name)
	defer parentSpan.End()

	// Use XREADGROUP to read messages from consumer group
	msgs, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    r.groupName,
		Consumer: r.consumerName,
		Streams:  []string{r.name, ">"},
		Count:    1,
		Block:    5 * time.Second,
	}).Result()
	if err == redis.Nil {
		return nil // idle
	}
	if err != nil {
		parentSpan.RecordError(err)
		parentSpan.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("failed to read from stream: %w", err)
	}
	if len(msgs) == 0 {
		return nil
	}

	var handlerErrs []error

	for _, msg := range msgs {
		for _, entry := range msg.Messages {
			// Skip init messages used for stream creation
			if _, isInit := entry.Values["init"]; isInit {
				// Acknowledge the init message to avoid PEL leaks, then delete it
				if err := r.client.XAck(ctx, r.name, r.groupName, entry.ID).Err(); err != nil {
					r.log.WithField("entryID", entry.ID).WithError(err).Warn("failed to ack init message")
				}
				if _, err := r.client.XDel(ctx, r.name, entry.ID).Result(); err != nil {
					r.log.WithField("entryID", entry.ID).WithError(err).Warn("failed to delete init message")
				}
				continue
			}

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
			bodyVal := entry.Values["body"]
			if bodyVal == nil {
				err := fmt.Errorf("body field is nil")
				handlerSpan.RecordError(err)
				handlerSpan.SetStatus(codes.Error, "body field is nil")

				// Ack then delete the bad message
				if err := r.client.XAck(ctx, r.name, r.groupName, entry.ID).Err(); err != nil {
					parentSpan.RecordError(err)
					parentSpan.SetStatus(codes.Error, err.Error())
				}
				if _, delErr := r.client.XDel(ctx, r.name, entry.ID).Result(); delErr != nil {
					parentSpan.RecordError(delErr)
					parentSpan.SetStatus(codes.Error, delErr.Error())
					handlerSpan.End()
					return fmt.Errorf("failed to delete message ID %s after body field is nil error: %w", entry.ID, delErr)
				}

				handlerSpan.End()
				handlerErrs = append(handlerErrs, err)
				continue
			}

			switch v := bodyVal.(type) {
			case []byte:
				body = v
			case string:
				body = []byte(v)
			default:
				err := fmt.Errorf("unexpected body type %T", v)
				handlerSpan.RecordError(err)
				handlerSpan.SetStatus(codes.Error, "unexpected body type")

				// Ack then delete the bad message
				if err := r.client.XAck(ctx, r.name, r.groupName, entry.ID).Err(); err != nil {
					parentSpan.RecordError(err)
					parentSpan.SetStatus(codes.Error, err.Error())
				}
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
			if err := handler(receivedCtx, body, entry.ID, r, log); err != nil {
				handlerSpan.RecordError(err)
				handlerSpan.SetStatus(codes.Error, err.Error())
				handlerErrs = append(handlerErrs, fmt.Errorf("handler error on ID %s: %w", entry.ID, err))
			}

			// Note: We don't delete the message here anymore. The message will be deleted
			// when Complete() is called, or it will remain pending for timeout processing.
			// This allows ProcessTimedOutMessages to find pending messages that haven't been completed.

			handlerSpan.End()
		}
	}

	// After processing all entries, report handler errors if any
	if len(handlerErrs) > 0 {
		return fmt.Errorf("one or more handler errors occurred: %v", handlerErrs)
	}

	return nil
}

func (r *redisQueue) Complete(ctx context.Context, entryID string, body []byte, processingErr error) error {
	if r.closed.Load() {
		return errors.New("queue is closed")
	}
	// If processing failed, we need to add the message to the failed messages set
	if processingErr != nil {
		failedKey := fmt.Sprintf("failed_messages:%s", r.name)
		// Best-effort read of current retry count; default to 0 on error/miss
		currentRetryCount := 0
		if msgs, xrErr := r.client.XRange(ctx, r.name, entryID, entryID).Result(); xrErr == nil && len(msgs) > 0 {
			if retryCountVal, ok := msgs[0].Values["retryCount"]; ok {
				switch v := retryCountVal.(type) {
				case int:
					currentRetryCount = v
				case int64:
					currentRetryCount = int(v)
				case string:
					if count, parseErr := strconv.Atoi(v); parseErr == nil {
						currentRetryCount = count
					}
				}
			}
		} else {
			r.log.WithField("entryID", entryID).
				WithError(xrErr).
				Warn("could not read retryCount; defaulting to 0")
		}
		// Calculate new retry count and backoff delay
		newRetryCount := currentRetryCount + 1
		backoffDelay := calculateBackoff(newRetryCount, r.retryConfig)
		// Use Redis server time to avoid drift
		redisTime, err := r.client.Time(ctx).Result()
		if err != nil {
			return fmt.Errorf("failed to get Redis time: %w", err)
		}
		retryTimestamp := redisTime.Add(backoffDelay).UnixMicro()
		// Create new failed member with updated retry count
		failedMember := r.formatFailedMember(entryID, body, newRetryCount)
		// Add new member with retry timestamp
		_, err = r.client.ZAdd(ctx, failedKey, redis.Z{
			Score:  float64(retryTimestamp),
			Member: failedMember,
		}).Result()
		if err != nil {
			return fmt.Errorf("failed to add message to failed set: %w", err)
		}
		r.log.WithField("entryID", entryID).
			WithField("processID", r.processID).
			WithField("currentRetryCount", currentRetryCount).
			WithField("newRetryCount", newRetryCount).
			WithField("backoffDelay", backoffDelay).
			Info("message processing failed, added to failed set with exponential backoff")
	}
	// Ack first, then delete the message entry
	if err := r.client.XAck(ctx, r.name, r.groupName, entryID).Err(); err != nil {
		return fmt.Errorf("failed to ack message ID %s after completion: %w", entryID, err)
	}
	if _, err := r.client.XDel(ctx, r.name, entryID).Result(); err != nil {
		return fmt.Errorf("failed to delete message ID %s after completion: %w", entryID, err)
	}
	return nil
}

func (r *redisQueue) Close() {
	if r.closed.Swap(true) {
		return
	}
}

// ProcessTimedOutMessages processes timed out pending messages and moves them to the failed messages set
func (r *redisQueue) ProcessTimedOutMessages(ctx context.Context, timeout time.Duration, handler func(entryID, body string) error) (int, error) {
	if r.closed.Load() {
		return 0, errors.New("queue is closed")
	}

	failedKey := fmt.Sprintf("failed_messages:%s", r.name)
	timedOutCount := 0

	// Get pending messages using XPENDING
	pending, err := r.client.XPending(ctx, r.name, r.groupName).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get pending messages: %w", err)
	}

	if pending.Count == 0 {
		return 0, nil // No pending messages
	}

	// Get detailed pending messages that have been idle for at least the timeout duration
	pendingMsgs, err := r.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: r.name,
		Group:  r.groupName,
		Start:  "-",
		End:    "+",
		Count:  pending.Count,
		Idle:   timeout,
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get pending message details: %w", err)
	}

	// Process each timed out pending message
	for _, pendingMsg := range pendingMsgs {
		// Get the message from the stream to extract body
		msgs, err := r.client.XRange(ctx, r.name, pendingMsg.ID, pendingMsg.ID).Result()
		if err != nil {
			r.log.WithField("entryID", pendingMsg.ID).WithError(err).Warn("failed to get timed out message from stream, skipping")
			continue
		}
		if len(msgs) == 0 {
			r.log.WithField("entryID", pendingMsg.ID).Warn("timed out message not found in stream, skipping")
			continue
		}

		// Extract body from message
		bodyVal, ok := msgs[0].Values["body"]
		if !ok {
			r.log.WithField("entryID", pendingMsg.ID).Warn("timed out message missing body, acknowledging and deleting")
			_ = r.client.XAck(ctx, r.name, r.groupName, pendingMsg.ID).Err()
			_, _ = r.client.XDel(ctx, r.name, pendingMsg.ID).Result()
			continue
		}

		var body []byte
		switch v := bodyVal.(type) {
		case []byte:
			body = v
		case string:
			body = []byte(v)
		default:
			r.log.WithField("entryID", pendingMsg.ID).Warn("timed out message has invalid body type, acknowledging and deleting")
			_ = r.client.XAck(ctx, r.name, r.groupName, pendingMsg.ID).Err()
			_, _ = r.client.XDel(ctx, r.name, pendingMsg.ID).Result()
			continue
		}

		// Call the provided handler before processing
		if handler != nil {
			if err := handler(pendingMsg.ID, body); err != nil {
				r.log.WithField("entryID", pendingMsg.ID).WithField("processID", r.processID).WithError(err).Warn("handler failed for timed out message, continuing")
				// Continue processing other messages even if handler fails
			}
		}

		// Extract current retry count from message values
		currentRetryCount := 0
		if retryCountVal, ok := msgs[0].Values["retryCount"]; ok {
			switch v := retryCountVal.(type) {
			case int:
				currentRetryCount = v
			case int64:
				currentRetryCount = int(v)
			case string:
				if count, parseErr := strconv.Atoi(v); parseErr == nil {
					currentRetryCount = count
				}
			}
		}

		// Add to failed messages set with incremented retry count
		newRetryCount := currentRetryCount + 1
		failedMember := r.formatFailedMember(pendingMsg.ID, body, newRetryCount)
		backoff := calculateBackoff(newRetryCount, r.retryConfig)
		redisTime, _ := r.client.Time(ctx).Result()
		_, err = r.client.ZAdd(ctx, failedKey, redis.Z{
			Score:  float64(redisTime.Add(backoff).UnixMicro()),
			Member: failedMember,
		}).Result()
		if err != nil {
			r.log.WithField("entryID", pendingMsg.ID).WithField("processID", r.processID).WithError(err).Warn("failed to add timed out message to failed set, continuing")
			continue
		}

		// Ack first, then delete from the stream
		if err := r.client.XAck(ctx, r.name, r.groupName, pendingMsg.ID).Err(); err != nil {
			r.log.WithField("entryID", pendingMsg.ID).WithError(err).Warn("failed to acknowledge timed out message")
			continue
		}
		_, err = r.client.XDel(ctx, r.name, pendingMsg.ID).Result()
		if err != nil {
			r.log.WithField("entryID", pendingMsg.ID).WithField("processID", r.processID).WithError(err).Warn("failed to delete timed out message, continuing")
			continue
		}

		r.log.WithField("entryID", pendingMsg.ID).WithField("processID", r.processID).WithField("currentRetryCount", currentRetryCount).WithField("newRetryCount", newRetryCount).Info("moved timed out message to failed set")
		timedOutCount++
	}

	return timedOutCount, nil
}

// RetryFailedMessages processes failed messages that are ready for retry
// and moves them back to the stream for processing with exponential backoff
func (r *redisQueue) RetryFailedMessages(ctx context.Context, config RetryConfig) (int, error) {
	if r.closed.Load() {
		return 0, errors.New("queue is closed")
	}

	failedKey := fmt.Sprintf("failed_messages:%s", r.name)
	retriedCount := 0

	// Get failed messages that are ready for retry (score <= current time)
	redisTime, err := r.client.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get Redis time: %w", err)
	}

	// Fetch entries due for retry, including their original scores
	failedEntries, err := r.client.ZRangeByScoreWithScores(ctx, failedKey, &redis.ZRangeBy{
		Min: "-inf",
		Max: strconv.FormatInt(redisTime.UnixMicro(), 10), // ready for retry
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get failed messages: %w", err)
	}
	r.log.
		WithField("failedKey", failedKey).
		WithField("failedMessageCount", len(failedEntries)).
		Debug("Found failed messages for retry")
	for _, z := range failedEntries {
		// Ensure the member is a string
		failedMember, ok := z.Member.(string)
		if !ok {
			r.log.
				WithField("memberType", fmt.Sprintf("%T", z.Member)).
				Warn("invalid ZSET member type, skipping")
			continue
		}
		// Parse the stored entry
		entryID, body, processID, retryCount, err := r.parseFailedMember(failedMember)
		if err != nil {
			// Truncate preview to avoid logging huge payloads
			preview := failedMember
			if len(preview) > 128 {
				preview = preview[:128] + "..."
			}
			r.log.
				WithField("member_preview", preview).
				WithError(err).
				Warn("invalid failed message format, skipping")
			continue
		}
		// Claim this member so no other worker retries it
		rem, remErr := r.client.ZRem(ctx, failedKey, failedMember).Result()
		if remErr != nil {
			r.log.
				WithField("entryID", entryID).
				WithError(remErr).
				Error("failed to claim failed message from ZSET")
			continue
		}
		if rem == 0 {
			// Already claimed by another worker
			continue
		}
		// Re-publish to the stream using Redis server time
		values := map[string]interface{}{
			"body":       body,
			"timestamp":  redisTime.UnixMicro(),
			"retry":      "true",
			"retryCount": retryCount,
		}
		newEntryID, err := r.client.XAdd(ctx, &redis.XAddArgs{
			Stream: r.name,
			Values: values,
		}).Result()
		if err != nil {
			// Restore the failed member with its original score on error
			_, _ = r.client.ZAdd(ctx, failedKey, redis.Z{Score: z.Score, Member: failedMember}).Result()
			r.log.
				WithField("entryID", entryID).
				WithField("processID", processID).
				WithError(err).
				Error("failed to add retry message to stream; restored to failed set")
			continue
		}
		r.log.
			WithField("originalEntryID", entryID).
			WithField("newEntryID", newEntryID).
			WithField("processID", processID).
			WithField("retryCount", retryCount).
			Info("retried failed message")
		retriedCount++
	}

	return retriedCount, nil
}
