package queues

/*
Redis Provider Implementation - Stream Consumer Groups with Checkpoint Tracking

This provider implements a reliable message queue system using Redis Streams with native
consumer groups and atomic checkpoint advancement for event replay safety.

Data Structures:
1. Redis Stream: <queue_name>
   - Stores incoming messages with tracing context
   - Uses consumer groups for automatic message tracking

2. Consumer Group: <queue_name>:<group_name>
   - Automatically tracks pending messages
   - Provides XPENDING for timeout detection
   - Messages are deleted immediately after processing

3. Failed Messages Sorted Set: failed_messages:<queue_name>
   - Member: <streamName>|<entryID>|base64(<body>)|<processID>|<retryCount>
   - Score: retry timestamp (when message should be retried)
   - Stores failed messages with exponential backoff retry scheduling
   - Sorted by retry timestamp for efficient retry processing
   - processID included for audit purposes
   - retryCount tracks number of retry attempts for exponential backoff
   - body is base64 encoded to handle any characters including pipe characters
   - streamName qualification ensures consistency with in-flight tracking

4. In-Flight Tasks Sorted Set: in_flight_tasks
   - Member: <stream_name>|<redis_entry_id> or <stream_name>|<redis_entry_id>:completed
   - Score: message timestamp (microseconds)
   - Tracks task completion status for safe checkpoint advancement
   - Tasks marked with ":completed" suffix when successfully processed
   - Failed tasks remain unmarked to act as checkpoint barriers
   - Completed tasks cleaned up atomically when checkpoint advances past them
   - Stream name qualification prevents ID collisions across different streams

5. Global Checkpoint: global_checkpoint
   - Key stores latest safe checkpoint timestamp (RFC3339Nano format)
   - Updated atomically with task cleanup via Lua script
   - Represents latest timestamp where all prior tasks are completed

Message Flow:
1. Message published to Redis stream with tracing context and timestamp
2. Consumer reads message using XREADGROUP
3. Add message to in_flight_tasks set when processing starts
4. Handler processes message
5. Message completed:
   - If successful: mark task as completed with ":completed" suffix
   - If failed: task remains unmarked (acts as checkpoint barrier)
   - If permanently failed (max retries): mark as completed to allow progress
   - Always: XDEL to remove from stream

Checkpoint Advancement:
- Lua script atomically scans in-flight tasks to find safe checkpoint timestamp
- Checkpoint advances to latest completed task before any incomplete task
- Completed tasks before checkpoint are cleaned up in same atomic operation
- Failed tasks prevent checkpoint advancement until resolved or permanently failed

Crash Recovery:
- Pending messages automatically tracked by Redis consumer groups
- Failed messages preserved in sorted set for retry
- In-flight task tracking survives worker crashes
- Checkpoint safety prevents data loss during Redis failures

Performance Characteristics:
- O(1) for message completion marking (single ZADD operation)
- O(N) checkpoint advancement where N = in-flight tasks (typically small)
- Atomic checkpoint operations prevent race conditions
- Exponential backoff with jitter prevents thundering herd during outages
- Configurable retry limits and backoff parameters
*/
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
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

// Redis key constants
const (
	redisKeyInFlightTasks        = "in_flight_tasks"
	redisKeyGlobalCheckpoint     = "global_checkpoint"
	redisKeyFailedMessagesPrefix = "failed_messages"
)

type redisProvider struct {
	client      *redis.Client
	log         logrus.FieldLogger
	wg          *sync.WaitGroup
	queues      []*redisQueue
	channels    []*redisChannel
	stopped     atomic.Bool
	mu          sync.Mutex
	processID   string
	retryConfig RetryConfig
}

func NewRedisProvider(ctx context.Context, log logrus.FieldLogger, processID string, hostname string, port uint, password api.SecureString, retryConfig RetryConfig) (Provider, error) {
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
		Addr:            fmt.Sprintf("%s:%d", hostname, port),
		Password:        password.Value(),
		DB:              0,
		MaxRetries:      retryConfig.MaxRetries,
		MinRetryBackoff: retryConfig.BaseDelay,
		MaxRetryBackoff: retryConfig.MaxDelay,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		DialTimeout:     5 * time.Second,
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

	// Check for existing active queue (deduplication)
	for _, q := range r.queues {
		if q.name == queueName && !q.closed.Load() {
			r.log.WithField("queueName", queueName).Debug("reusing existing queue instance")
			return q, nil
		}
	}

	r.log.WithField("queueName", queueName).Debug("creating new queue instance")

	// Create consumer group name and consumer name
	groupName := fmt.Sprintf("%s-group", queueName)
	consumerName := fmt.Sprintf("%s-consumer-%s", queueName, r.processID)
	ql := r.log.WithFields(logrus.Fields{
		"consumerName":  consumerName,
		"consumerGroup": groupName,
	})

	queue := &redisQueue{
		name:         queueName,
		client:       r.client,
		groupName:    groupName,
		consumerName: consumerName,
		log:          ql,
		wg:           r.wg,
		processID:    r.processID,
		retryConfig:  r.retryConfig,
	}
	if err := queue.ensureConsumerGroup(ctx); err != nil {
		return nil, err
	}

	r.queues = append(r.queues, queue)
	return queue, nil
}

func (r *redisProvider) newChannel(ctx context.Context, channelName string) (*redisChannel, error) {
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

func (r *redisProvider) NewQueueConsumer(ctx context.Context, queueName string) (QueueConsumer, error) {
	return r.newQueue(ctx, queueName)
}

func (r *redisProvider) NewQueueProducer(ctx context.Context, queueName string) (QueueProducer, error) {
	return r.newQueue(ctx, queueName)
}

func (r *redisProvider) NewPubSubPublisher(ctx context.Context, channelName string) (PubSubPublisher, error) {
	return r.newChannel(ctx, channelName)
}

func (r *redisProvider) NewPubSubSubscriber(ctx context.Context, channelName string) (PubSubSubscriber, error) {
	return r.newChannel(ctx, channelName)
}

func (r *redisProvider) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Swap(true) {
		return
	}
	// Signal all queue goroutines to exit.
	for _, q := range r.queues {
		r.log.WithField("queueName", q.name).Debug("closing queue instance")
		q.Close()
	}
	defer r.wg.Done()

	// Close all channels
	for _, channel := range r.channels {
		r.log.WithField("channelName", channel.name).Debug("closing channel instance")
		channel.Close()
	}

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

func (r *redisProvider) GetLatestProcessedTimestamp(ctx context.Context) (time.Time, error) {
	// Get the current checkpoint timestamp from Redis
	checkpointStr, err := r.client.Get(ctx, redisKeyGlobalCheckpoint).Result()
	if err == redis.Nil {
		// Checkpoint key is missing from Redis
		return time.Time{}, ErrCheckpointMissing
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get checkpoint: %w", err)
	}

	// Parse the checkpoint timestamp - try integer first, then float (for scientific notation), then RFC3339Nano
	if microseconds, err := strconv.ParseInt(checkpointStr, 10, 64); err == nil {
		// Stored as integer microseconds (best precision)
		return time.UnixMicro(microseconds), nil
	}
	if microsecondsFloat, err := strconv.ParseFloat(checkpointStr, 64); err == nil {
		// Stored as microseconds in scientific notation
		// Use math.Round to avoid precision loss when converting float64 to int64
		return time.UnixMicro(int64(math.Round(microsecondsFloat))), nil
	}

	// Try RFC3339Nano format
	timestamp, err := time.Parse(time.RFC3339Nano, checkpointStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse checkpoint timestamp: %w", err)
	}

	return timestamp, nil
}

func (r *redisProvider) AdvanceCheckpointAndCleanup(ctx context.Context) error {
	// Lua script to find safe checkpoint timestamp and update atomically
	luaScript := `
		local checkpointKey = KEYS[1]
		local inFlightKey = KEYS[2]

		-- Check if checkpoint key exists, exit if missing
		if redis.call('EXISTS', checkpointKey) == 0 then
			return {-1, 0, "checkpoint key missing"}
		end

		-- Get current checkpoint
		local currentCheckpoint = redis.call('GET', checkpointKey)

		-- Get all in-flight tasks in timestamp order
		local allTasks = redis.call('ZRANGE', inFlightKey, 0, -1, 'WITHSCORES')

		-- If no tasks at all, nothing to checkpoint
		if #allTasks == 0 then
			return {0, 0, "no in-flight tasks found"}
		end

		-- Find the first incomplete task
		local safeTimestampNum = nil
		local completedCount = 0
		local incompleteCount = 0
		local firstIncomplete = nil

		for i = 1, #allTasks, 2 do
			local member = allTasks[i]
			local score = tonumber(allTasks[i+1])

			-- If task is completed, we can checkpoint at this timestamp
			if string.sub(member, -10) == ":completed" then
				safeTimestampNum = score
				completedCount = completedCount + 1
			else
				-- Hit incomplete task, stop here
				incompleteCount = incompleteCount + 1
				if not firstIncomplete then
					firstIncomplete = member
				end
				break
			end
		end

		-- If no safe timestamp found, nothing to checkpoint
		if not safeTimestampNum then
			return {0, 0, "no completed tasks found"}
		end

		-- Check if new timestamp is actually newer than current checkpoint
		if currentCheckpoint then
			local currentCheckpointNum = tonumber(currentCheckpoint)
			if currentCheckpointNum and safeTimestampNum <= currentCheckpointNum then
				return {0, 0, "timestamp not newer than current checkpoint"}
			end
		end

		-- Update checkpoint (format as integer to preserve precision)
		redis.call('SET', checkpointKey, string.format("%.0f", safeTimestampNum))

		-- Clean up completed tasks before the new checkpoint
		local safeTimestampMicros = safeTimestampNum
		local completedTasks = redis.call('ZRANGEBYSCORE', inFlightKey, '-inf', safeTimestampMicros)

		local cleanedCount = 0
		for i, member in ipairs(completedTasks) do
			-- Only remove if it's a completed task (has ":completed" suffix)
			if string.sub(member, -10) == ":completed" then
				redis.call('ZREM', inFlightKey, member)
				cleanedCount = cleanedCount + 1
			end
		end

		return {1, cleanedCount, string.format("%.0f", safeTimestampNum)} -- 1 = updated, cleanedCount, new timestamp
	`

	// Execute the Lua script
	result, err := r.client.Eval(ctx, luaScript, []string{redisKeyGlobalCheckpoint, redisKeyInFlightTasks}).Result()
	if err != nil {
		return fmt.Errorf("failed to execute checkpoint advancement script: %w", err)
	}

	// Parse result
	resultSlice, ok := result.([]interface{})
	if !ok || len(resultSlice) != 3 {
		return fmt.Errorf("unexpected script result format: %v", result)
	}

	updated := resultSlice[0].(int64)
	cleanedCount := resultSlice[1].(int64)
	message := resultSlice[2].(string)

	if updated == -1 {
		// Checkpoint key is missing
		return ErrCheckpointMissing
	} else if updated == 1 {
		r.log.WithField("newCheckpoint", message).
			WithField("cleanedTasks", cleanedCount).
			Info("Advanced checkpoint and cleaned up completed tasks")
	} else {
		r.log.WithField("reason", message).
			Debug("Checkpoint not advanced")
	}

	return nil
}

func (r *redisProvider) SetCheckpointTimestamp(ctx context.Context, timestamp time.Time) error {
	// Store timestamp as integer microseconds for precision
	var timestampStr string
	if timestamp.IsZero() {
		timestampStr = "0"
	} else {
		timestampStr = strconv.FormatInt(timestamp.UnixMicro(), 10)
	}

	err := r.client.Set(ctx, redisKeyGlobalCheckpoint, timestampStr, 0).Err()
	if err != nil {
		return fmt.Errorf("failed to set checkpoint timestamp: %w", err)
	}

	r.log.WithField("timestamp", timestamp.Format(time.RFC3339Nano)).Debug("Set checkpoint timestamp in Redis")
	return nil
}

func (r *redisProvider) ProcessTimedOutMessages(ctx context.Context, queueName string, timeout time.Duration, handler func(entryID string, body []byte) error) (int, error) {
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

func (r *redisProvider) RetryFailedMessages(ctx context.Context, queueName string, config RetryConfig, handler func(entryID string, body []byte, retryCount int) error) (int, error) {
	// Find existing queue or create a temporary one
	queue := r.findExistingQueue(queueName)

	if queue == nil {
		var err error
		queue, err = r.newQueue(ctx, queueName)
		if err != nil {
			return 0, err
		}
	}
	return queue.RetryFailedMessages(ctx, config, handler)
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

// ensureConsumerGroup creates the consumer group or recreates if it was lost (e.g., after Redis restart).
func (r *redisQueue) ensureConsumerGroup(ctx context.Context) error {
	// This is idempotent - no need to synchronize
	if err := r.client.XGroupCreateMkStream(ctx, r.name, r.groupName, "0").Err(); err != nil {
		// BUSYGROUP means it already exists - treat as success
		if strings.Contains(err.Error(), "BUSYGROUP") {
			r.log.Info("consumer group already exists")
			return nil
		}
		return fmt.Errorf("failed to create consumer group: %w", err)
	}
	r.log.Info("consumer group ensured")
	return nil
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
// Format: "<streamName>|<entryID>|base64(body)|processID|retryCount"
// Input: entryID (string), body (bytes), retryCount (int)
// Returns: formatted string
func (r *redisQueue) formatFailedMember(entryID string, body []byte, retryCount int) string {
	// Base64 encode the body to handle any characters including pipe characters
	encodedBody := base64.StdEncoding.EncodeToString(body)
	// Qualify entryID with stream name for consistency with in-flight tracking
	qualifiedEntryID := fmt.Sprintf("%s|%s", r.name, entryID)
	return fmt.Sprintf("%s|%s|%s|%d", qualifiedEntryID, encodedBody, r.processID, retryCount)
}

// parseFailedMember parses a failed member with retry count.
// Format: "<streamName>|<entryID>|base64(body)|processID|retryCount"
// Returns: streamName (string), entryID (string), body (bytes), processID (string), retryCount (int), error
func (r *redisQueue) parseFailedMember(failedMember string) (string, string, []byte, string, int, error) {
	parts := strings.SplitN(failedMember, "|", 5)
	if len(parts) != 5 {
		return "", "", nil, "", 0, fmt.Errorf("invalid failed member format: %s", failedMember)
	}

	retryCount, err := strconv.Atoi(parts[4])
	if err != nil {
		return "", "", nil, "", 0, fmt.Errorf("invalid retry count in failed member: %s", failedMember)
	}

	// Decode the base64 body
	decodedBody, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", "", nil, "", 0, fmt.Errorf("failed to decode base64 body in failed member: %s", failedMember)
	}

	// Extract the stream name and original entryID
	// parts[0] = streamName, parts[1] = entryID
	streamName := parts[0]
	entryID := parts[1]
	processID := parts[3]

	return streamName, entryID, decodedBody, processID, retryCount, nil // streamName, entryID, body, processID, retryCount
}

func (r *redisQueue) Enqueue(ctx context.Context, payload []byte, timestamp int64) error {
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
	// Retry on NOGROUP error to handle race condition where consumer group isn't ready yet
	var msgs []redis.XStream
	var err error
	maxRetries := 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		msgs, err = r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
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
			// Check if this is a NOGROUP error (consumer group doesn't exist yet)
			if strings.Contains(err.Error(), "NOGROUP") {
				if attempt < maxRetries-1 {
					r.log.WithError(err).WithField("attempt", attempt+1).Debug("consumer group not ready, retrying")
					// Use exponential backoff for retries to allow group creation to propagate
					infraRetryConfig := RetryConfig{
						BaseDelay:    50 * time.Millisecond,
						MaxDelay:     500 * time.Millisecond,
						JitterFactor: 0.2,
					}
					delay := calculateBackoff(attempt, infraRetryConfig)
					timer := time.NewTimer(delay)
					select {
					case <-ctx.Done():
						timer.Stop()
						return nil
					case <-timer.C:
					}
					continue
				}
				// All retries exhausted
				r.log.WithError(err).Error("consumer group not found after retries")
				return nil // Will read messages on next consume iteration
			}
			parentSpan.RecordError(err)
			parentSpan.SetStatus(codes.Error, err.Error())
			return fmt.Errorf("failed to read from stream: %w", err)
		}
		break // Success, exit retry loop
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

			// Add to in-flight tasks before processing using message timestamp
			timestamp := entry.Values["timestamp"]
			originalEntryID := entry.Values["originalEntryID"]
			if err := r.addToInFlightTasks(receivedCtx, entry.ID, timestamp, originalEntryID); err != nil {
				r.log.WithError(err).WithField("entryID", entry.ID).Debug("failed to add to in-flight tasks, continuing processing")
			}

			// Run handler and collect error if occurs
			if err := handler(receivedCtx, body, entry.ID, r, log); err != nil {
				handlerSpan.RecordError(err)
				handlerSpan.SetStatus(codes.Error, err.Error())
				handlerErrs = append(handlerErrs, fmt.Errorf("handler error on ID %s: %w", entry.ID, err))
			}

			// Note: in-flight task removal happens in Complete() method after message acknowledgment

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
		failedKey := fmt.Sprintf("%s:%s", redisKeyFailedMessagesPrefix, r.name)
		// Best-effort read of current retry count; default to 0 on error/miss
		currentRetryCount := 0
		if msgs, xrErr := r.client.XRange(ctx, r.name, entryID, entryID).Result(); xrErr == nil && len(msgs) > 0 {
			if retryCountVal, ok := msgs[0].Values["retryCount"]; ok {
				currentRetryCount = extractRetryCountFromValue(retryCountVal)
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
			r.log.WithError(err).Warn("failed to get Redis time; falling back to local clock for backoff scheduling")
			redisTime = time.Now()
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
	// Get originalEntryID and timestamp from message before deletion for in-flight cleanup
	var trackingEntryID string = entryID // Default to current entry ID
	var messageTimestamp float64
	if msgs, xrErr := r.client.XRange(ctx, r.name, entryID, entryID).Result(); xrErr == nil && len(msgs) > 0 {
		if originalID, ok := msgs[0].Values["originalEntryID"]; ok {
			if origIDStr, ok := originalID.(string); ok && origIDStr != "" {
				trackingEntryID = origIDStr
			}
		}
		// Extract timestamp from message for completion tracking
		if timestamp, ok := msgs[0].Values["timestamp"]; ok {
			switch v := timestamp.(type) {
			case string:
				if ts, parseErr := strconv.ParseInt(v, 10, 64); parseErr == nil {
					messageTimestamp = float64(ts)
				}
			case int64:
				messageTimestamp = float64(v)
			case int:
				messageTimestamp = float64(v)
			default:
				r.log.WithField("entryID", entryID).WithField("timestampType", fmt.Sprintf("%T", v)).Debug("unexpected timestamp type in message")
			}
		}
	}

	// Ack first, then delete the message entry
	if err := r.client.XAck(ctx, r.name, r.groupName, entryID).Err(); err != nil {
		return fmt.Errorf("failed to ack message ID %s after completion: %w", entryID, err)
	}
	if _, err := r.client.XDel(ctx, r.name, entryID).Result(); err != nil {
		return fmt.Errorf("failed to delete message ID %s after completion: %w", entryID, err)
	}

	// Mark task completion based on processing result
	if processingErr == nil {
		// Successful completion - mark as completed for checkpoint tracking
		r.markInFlightTaskComplete(ctx, trackingEntryID, messageTimestamp)
	}
	// Note: Failed tasks remain in in_flight_tasks as incomplete (no ":completed" suffix)
	// This ensures they act as barriers preventing checkpoint advancement past their timestamp

	return nil
}

// extractRetryCountFromValue extracts retry count from a message value
func extractRetryCountFromValue(retryCountVal interface{}) int {
	switch v := retryCountVal.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if count, parseErr := strconv.Atoi(v); parseErr == nil {
			return count
		}
	}
	return 0
}

// addToInFlightTasks adds a message to the in-flight tasks tracking set
func (r *redisQueue) addToInFlightTasks(ctx context.Context, entryID string, timestamp interface{}, originalEntryID interface{}) error {
	// Extract timestamp from message (should be microseconds)
	var score float64
	switch v := timestamp.(type) {
	case int64:
		score = float64(v)
	case int:
		score = float64(v)
	case string:
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
			score = float64(ts)
		} else {
			return fmt.Errorf("invalid timestamp format: %s", v)
		}
	default:
		return fmt.Errorf("unsupported timestamp type: %T", v)
	}

	// Use original entry ID if this is a retry, otherwise use current entry ID
	memberID := entryID
	if originalEntryID != nil {
		if origID, ok := originalEntryID.(string); ok && origID != "" {
			memberID = origID
		}
	}

	// Qualify the member ID with stream name to avoid collisions across streams
	qualifiedMemberID := fmt.Sprintf("%s|%s", r.name, memberID)

	// Add to in-flight tasks sorted set
	err := r.client.ZAdd(ctx, redisKeyInFlightTasks, redis.Z{
		Score:  score,
		Member: qualifiedMemberID,
	}).Err()

	return err
}

// markInFlightTaskComplete marks a task as completed by modifying its member ID in-place
func (r *redisQueue) markInFlightTaskComplete(ctx context.Context, entryID string, timestamp float64) {
	if entryID == "" {
		return
	}

	// Qualify the entry ID with stream name to match the qualified member ID used in addToInFlightTasks
	qualifiedEntryID := fmt.Sprintf("%s|%s", r.name, entryID)
	r.markInFlightTaskCompleteWithQualifiedID(ctx, qualifiedEntryID, timestamp)
}

// markInFlightTaskCompleteWithQualifiedID marks a task as completed using a pre-qualified entry ID
func (r *redisQueue) markInFlightTaskCompleteWithQualifiedID(ctx context.Context, qualifiedEntryID string, timestamp float64) {
	if qualifiedEntryID == "" {
		return
	}

	r.log.WithField("qualifiedEntryID", qualifiedEntryID).WithField("timestamp", timestamp).Debug("marking in-flight task as completed")

	// Use Lua script to atomically remove original entry and add completed entry
	luaScript := `
		local key = KEYS[1]
		local entryID = ARGV[1]
		local timestamp = tonumber(ARGV[2])

		-- Remove the original entry
		redis.call('ZREM', key, entryID)

		-- Add the completed entry
		redis.call('ZADD', key, timestamp, entryID .. ':completed')

		return 1
	`

	err := r.client.Eval(ctx, luaScript, []string{redisKeyInFlightTasks}, qualifiedEntryID, timestamp).Err()

	if err != nil {
		r.log.WithError(err).WithField("qualifiedEntryID", qualifiedEntryID).Debug("failed to mark in-flight task as completed")
	} else {
		r.log.WithField("qualifiedEntryID", qualifiedEntryID).Debug("successfully marked in-flight task as completed")
	}
}

func (r *redisQueue) Close() {
	if r.closed.Swap(true) {
		return
	}
}

// ProcessTimedOutMessages processes timed out pending messages and moves them to the failed messages set.
// Note: Some pending messages may no longer exist in the stream (e.g., if they were already processed
// and deleted by another process). In such cases, we acknowledge them to remove them from the pending list.
func (r *redisQueue) ProcessTimedOutMessages(ctx context.Context, timeout time.Duration, handler func(entryID string, body []byte) error) (int, error) {
	if r.closed.Load() {
		return 0, errors.New("queue is closed")
	}

	failedKey := fmt.Sprintf("failed_messages:%s", r.name)
	timedOutCount := 0

	// Get pending messages using XPENDING
	pending, err := r.client.XPending(ctx, r.name, r.groupName).Result()
	if err != nil {
		if strings.Contains(err.Error(), "NOGROUP") {
			r.log.Warnf("consumer group %s does not exist (lost?), recreating", r.groupName)
			if errGroup := r.ensureConsumerGroup(ctx); errGroup != nil {
				return 0, errGroup
			}
			// retry after recreation
			pending, err = r.client.XPending(ctx, r.name, r.groupName).Result()
		}
		if err != nil {
			return 0, fmt.Errorf("failed to get pending messages: %w", err)
		}
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
			// Acknowledge the pending message to remove it from pending list
			if ackErr := r.client.XAck(ctx, r.name, r.groupName, pendingMsg.ID).Err(); ackErr != nil {
				r.log.WithField("entryID", pendingMsg.ID).WithError(ackErr).Warn("failed to acknowledge pending message")
			}
			continue
		}
		if len(msgs) == 0 {
			r.log.WithField("entryID", pendingMsg.ID).Debug("timed out message not found in stream, acknowledging pending message")
			// Message was already deleted from stream, acknowledge it to remove from pending list
			if ackErr := r.client.XAck(ctx, r.name, r.groupName, pendingMsg.ID).Err(); ackErr != nil {
				r.log.WithField("entryID", pendingMsg.ID).WithError(ackErr).Warn("failed to acknowledge pending message")
			}
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
			currentRetryCount = extractRetryCountFromValue(retryCountVal)
		}

		// Add to failed messages set with incremented retry count
		newRetryCount := currentRetryCount + 1
		failedMember := r.formatFailedMember(pendingMsg.ID, body, newRetryCount)
		backoff := calculateBackoff(newRetryCount, r.retryConfig)
		redisTime, tErr := r.client.Time(ctx).Result()
		if tErr != nil {
			r.log.WithError(tErr).Warn("failed to get Redis time; falling back to local clock for backoff scheduling")
			redisTime = time.Now()
		}
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
func (r *redisQueue) RetryFailedMessages(ctx context.Context, config RetryConfig, handler func(entryID string, body []byte, retryCount int) error) (int, error) {
	if r.closed.Load() {
		return 0, errors.New("queue is closed")
	}

	failedKey := fmt.Sprintf("failed_messages:%s", r.name)
	retriedCount := 0

	// Get failed messages that are ready for retry (score <= current time)
	redisTime, err := r.client.Time(ctx).Result()
	if err != nil {
		r.log.WithError(err).Warn("failed to get Redis time; falling back to local clock for backoff scheduling")
		redisTime = time.Now()
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
		streamName, entryID, body, processID, retryCount, err := r.parseFailedMember(failedMember)
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

		// Check if we've exceeded max retries
		if retryCount >= config.MaxRetries {
			r.log.WithField("entryID", entryID).
				WithField("processID", processID).
				WithField("retryCount", retryCount).
				Warn("message exceeded max retries, removing from failed set")

			// Call the provided handler before processing
			if handler != nil {
				if err := handler(entryID, body, retryCount); err != nil {
					r.log.WithField("entryID", entryID).WithField("processID", r.processID).WithError(err).Warn("handler failed for permanently failed message, continuing")
					// Continue processing other messages even if handler fails
				}
			}

			// Remove from failed messages set (permanent failure)
			_, err = r.client.ZRem(ctx, failedKey, failedMember).Result()
			if err != nil {
				r.log.WithField("entryID", entryID).WithError(err).
					Error("failed to remove permanently failed message from failed set")
			}

			// Mark task as completed in in-flight tracking so checkpoint can advance past it
			// Use current time as timestamp since we're permanently failing this task
			// Use qualified entry ID with original stream name for in-flight tracking
			qualifiedEntryID := fmt.Sprintf("%s|%s", streamName, entryID)
			r.markInFlightTaskCompleteWithQualifiedID(ctx, qualifiedEntryID, float64(time.Now().UnixMicro()))

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
		// Re-publish to the original stream using Redis server time
		values := map[string]interface{}{
			"body":            body,
			"timestamp":       redisTime.UnixMicro(),
			"retry":           "true",
			"retryCount":      retryCount,
			"originalEntryID": entryID, // Preserve original entry ID for in-flight tracking
		}
		newEntryID, err := r.client.XAdd(ctx, &redis.XAddArgs{
			Stream: streamName, // Retry to the original stream where the message failed
			Values: values,
		}).Result()
		if err != nil {
			// Restore the failed member with its original score on error
			_, _ = r.client.ZAdd(ctx, failedKey, redis.Z{Score: z.Score, Member: failedMember}).Result()
			r.log.
				WithField("originalStream", streamName).
				WithField("entryID", entryID).
				WithField("processID", processID).
				WithError(err).
				Error("failed to add retry message to original stream; restored to failed set")
			continue
		}
		r.log.
			WithField("originalStream", streamName).
			WithField("originalEntryID", entryID).
			WithField("newEntryID", newEntryID).
			WithField("processID", processID).
			WithField("retryCount", retryCount).
			Info("retried failed message to original stream")
		retriedCount++
	}

	return retriedCount, nil
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
