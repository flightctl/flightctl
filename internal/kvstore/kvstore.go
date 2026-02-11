package kvstore

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// StreamEntry represents a single entry in a Redis stream
type StreamEntry struct {
	ID    string
	Value []byte
}

type KVStore interface {
	Close()
	SetNX(ctx context.Context, key string, value []byte) (bool, error)
	SetIfGreater(ctx context.Context, key string, newVal int64) (bool, error)
	Get(ctx context.Context, key string) ([]byte, error)
	GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error)
	DeleteKeysForTemplateVersion(ctx context.Context, key string) error
	DeleteAllKeys(ctx context.Context) error
	PrintAllKeys(ctx context.Context) // For debugging
	// Stream operations for log streaming
	StreamAdd(ctx context.Context, key string, value []byte) (string, error)
	StreamRange(ctx context.Context, key string, start, stop string) ([]StreamEntry, error)
	StreamRead(ctx context.Context, key string, lastID string, block time.Duration, count int64) ([]StreamEntry, error)
	SetExpire(ctx context.Context, key string, expiration time.Duration) error
	Delete(ctx context.Context, key string) error
}

type kvStore struct {
	log                logrus.FieldLogger
	client             *redis.Client
	getSetNxScript     *redis.Script
	setIfGreaterScript *redis.Script
}

func NewKVStore(ctx context.Context, log logrus.FieldLogger, hostname string, port uint, password domain.SecureString) (KVStore, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/kvstore", "KVStore")
	defer span.End()

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
		return nil, fmt.Errorf("failed to connect to KV store: %w", err)
	}
	log.Debug("successfully connected to the KV store")

	// Lua script to get the value if it exists, otherwise set and return it
	luaScript := redis.NewScript(`
		local value = redis.call('get', KEYS[1])
		if not value then
			redis.call('set', KEYS[1], ARGV[1], 'NX')
			value = ARGV[1]
		end
		return value
	`)

	// Lua script to set the key to a new value only if the current value is less than the new value
	setIfGreaterScript := redis.NewScript(`
local current = tonumber(redis.call("get", KEYS[1]))
local newval = tonumber(ARGV[1])
if not current or newval > current then
    redis.call("set", KEYS[1], newval)
    return 1
else
    return 0
end
`)

	return &kvStore{
		log:                log,
		client:             client,
		getSetNxScript:     luaScript,
		setIfGreaterScript: setIfGreaterScript,
	}, nil
}

func (s *kvStore) Close() {
	err := s.client.Close()
	if err != nil {
		s.log.Errorf("failed closing connection to KV store: %v", err)
	}
}

func (s *kvStore) DeleteAllKeys(ctx context.Context) error {
	_, err := s.client.FlushAll(ctx).Result()
	if err != nil {
		return fmt.Errorf("failed deleting all keys: %w", err)
	}
	return nil
}

// Sets the key to value only if the key does Not eXist. Returns a boolean indicating if the value was updated by this call.
func (s *kvStore) SetNX(ctx context.Context, key string, value []byte) (bool, error) {
	success, err := s.client.SetNX(ctx, key, value, 0).Result()
	if err != nil {
		return false, fmt.Errorf("failed storing key: %w", err)
	}
	return success, nil
}

// Sets the key to value, only if the key does not already exist or if its current value is less than the new value.
func (s *kvStore) SetIfGreater(ctx context.Context, key string, newVal int64) (bool, error) {
	res, err := s.setIfGreaterScript.Run(ctx, s.client, []string{key}, newVal).Result()
	if err != nil {
		return false, err
	}
	return res.(int64) == 1, nil
}

// Gets the value for the specified key.
func (s *kvStore) Get(ctx context.Context, key string) ([]byte, error) {
	result, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed getting key: %w", err)
	}
	return result, nil
}

func (s *kvStore) GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error) {
	result, err := s.getSetNxScript.Run(ctx, s.client, []string{key}, value).Result()
	if err != nil {
		return nil, fmt.Errorf("failed executing GetOrSetNX: %w", err)
	}

	// Convert the result to a byte slice
	switch v := result.(type) {
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
	default:
		return nil, fmt.Errorf("unexpected type for result: %T", result)
	}
}

func (s *kvStore) DeleteKeysForTemplateVersion(ctx context.Context, key string) error {
	pattern := fmt.Sprintf("%s*", key)
	iter := s.client.Scan(ctx, 0, pattern, 0).Iterator()

	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("failed listing keys: %w", err)
	}

	if len(keys) > 0 {
		if err := s.client.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("failed deleting keys: %w", err)
		}
	}

	return nil
}

func (s *kvStore) PrintAllKeys(ctx context.Context) {
	var keys []string
	iter := s.client.Scan(ctx, 0, "*", 0).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		fmt.Printf("failed listing keys: %v\n", err)
		return
	}
	fmt.Printf("Keys: %v\n", keys)
}

// StreamAdd adds a value to a Redis stream and returns the message ID
// Uses "*" to auto-generate ID (timestamp-based)
func (s *kvStore) StreamAdd(ctx context.Context, key string, value []byte) (string, error) {
	id, err := s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		ID:     "*", // Auto-generate ID
		Values: map[string]interface{}{
			"log": value,
		},
	}).Result()
	if err != nil {
		return "", fmt.Errorf("failed to add to stream: %w", err)
	}
	return id, nil
}

// StreamRange returns a range of entries from a Redis stream
// start and stop can be "-" (beginning), "+" (end), or specific message IDs
func (s *kvStore) StreamRange(ctx context.Context, key string, start, stop string) ([]StreamEntry, error) {
	entries, err := s.client.XRange(ctx, key, start, stop).Result()
	if err != nil {
		if err == redis.Nil {
			return []StreamEntry{}, nil
		}
		return nil, fmt.Errorf("failed to get stream range: %w", err)
	}

	result := make([]StreamEntry, 0, len(entries))
	for _, entry := range entries {
		// Extract the "log" field from the stream entry
		logValue, ok := entry.Values["log"].(string)
		if !ok {
			// Try as []byte
			if logBytes, ok := entry.Values["log"].([]byte); ok {
				result = append(result, StreamEntry{
					ID:    entry.ID,
					Value: logBytes,
				})
				continue
			}
			return nil, fmt.Errorf("unexpected type for log value: %T", entry.Values["log"])
		}
		result = append(result, StreamEntry{
			ID:    entry.ID,
			Value: []byte(logValue),
		})
	}
	return result, nil
}

// StreamRead reads entries from a Redis stream with blocking support
// lastID is the last message ID read (use "0" to read from beginning, "$" for new messages only)
// block is the blocking timeout (0 for non-blocking)
// count limits the number of entries returned (0 for no limit)
func (s *kvStore) StreamRead(ctx context.Context, key string, lastID string, block time.Duration, count int64) ([]StreamEntry, error) {
	args := &redis.XReadArgs{
		Streams: []string{key, lastID},
		Count:   count,
	}
	if block > 0 {
		args.Block = block
	}

	streams, err := s.client.XRead(ctx, args).Result()
	if err != nil {
		if err == redis.Nil {
			// Timeout or no data available
			return []StreamEntry{}, nil
		}
		return nil, fmt.Errorf("failed to read from stream: %w", err)
	}

	if len(streams) == 0 {
		return []StreamEntry{}, nil
	}

	// Extract entries from the first stream
	stream := streams[0]
	result := make([]StreamEntry, 0, len(stream.Messages))
	for _, msg := range stream.Messages {
		// Extract the "log" field from the stream entry
		logValue, ok := msg.Values["log"].(string)
		if !ok {
			// Try as []byte
			if logBytes, ok := msg.Values["log"].([]byte); ok {
				result = append(result, StreamEntry{
					ID:    msg.ID,
					Value: logBytes,
				})
				continue
			}
			return nil, fmt.Errorf("unexpected type for log value: %T", msg.Values["log"])
		}
		result = append(result, StreamEntry{
			ID:    msg.ID,
			Value: []byte(logValue),
		})
	}
	return result, nil
}

// SetExpire sets an expiration time on a key
func (s *kvStore) SetExpire(ctx context.Context, key string, expiration time.Duration) error {
	if err := s.client.Expire(ctx, key, expiration).Err(); err != nil {
		return fmt.Errorf("failed to set expiration: %w", err)
	}
	return nil
}

// Delete deletes a key from Redis
func (s *kvStore) Delete(ctx context.Context, key string) error {
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete key: %w", err)
	}
	return nil
}
