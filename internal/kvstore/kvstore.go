package kvstore

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

type KVStore interface {
	Close()
	SetNX(ctx context.Context, key string, value []byte) (bool, error)
	Get(ctx context.Context, key string) ([]byte, error)
	GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error)
	DeleteKeysForTemplateVersion(ctx context.Context, key string) error
	DeleteAllKeys(ctx context.Context) error
	PrintAllKeys(ctx context.Context) // For debugging
}

type kvStore struct {
	log            logrus.FieldLogger
	client         *redis.Client
	getSetNxScript *redis.Script
}

func NewKVStore(ctx context.Context, log logrus.FieldLogger, hostname string, port uint, password string) (KVStore, error) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/kvstore", "KVStore")
	defer span.End()

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
		return nil, fmt.Errorf("failed to connect to KV store: %w", err)
	}
	log.Info("successfully connected to the KV store")

	// Lua script to get the value if it exists, otherwise set and return it
	luaScript := redis.NewScript(`
		local value = redis.call('get', KEYS[1])
		if not value then
			redis.call('set', KEYS[1], ARGV[1], 'NX')
			value = ARGV[1]
		end
		return value
	`)

	return &kvStore{
		log:            log,
		client:         client,
		getSetNxScript: luaScript,
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
