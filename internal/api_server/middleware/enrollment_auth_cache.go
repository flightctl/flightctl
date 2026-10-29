package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
)

const DefaultEnrollmentCSRStatusCacheTTL = 30 * time.Second
const missingCSRStatusValue = "null"

type enrollmentCSRStatusCacheEntry struct {
	Name   *string                                 `json:"name,omitempty"`
	Status *domain.CertificateSigningRequestStatus `json:"status,omitempty"`
}

type RedisEnrollmentCSRStatusCache struct {
	kvStore kvstore.KVStore
	ttl     time.Duration
}

func NewRedisEnrollmentCSRStatusCache(kvStore kvstore.KVStore, ttl time.Duration) (*RedisEnrollmentCSRStatusCache, error) {
	if kvStore == nil {
		return nil, errors.New("kvStore is required")
	}
	if ttl <= 0 {
		ttl = DefaultEnrollmentCSRStatusCacheTTL
	}
	return &RedisEnrollmentCSRStatusCache{kvStore: kvStore, ttl: ttl}, nil
}

func (c *RedisEnrollmentCSRStatusCache) Get(ctx context.Context, orgId uuid.UUID, certificateDigest string) (enrollmentCSRStatusCacheEntry, bool, error) {
	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	value, err := c.kvStore.Get(ctx, key)
	if err != nil {
		return enrollmentCSRStatusCacheEntry{}, false, fmt.Errorf("getting cached enrollment CSR status: %w", err)
	}
	if len(value) == 0 {
		return enrollmentCSRStatusCacheEntry{}, false, nil
	}
	if string(value) == missingCSRStatusValue {
		return enrollmentCSRStatusCacheEntry{}, true, nil
	}

	var entry enrollmentCSRStatusCacheEntry
	if err := json.Unmarshal(value, &entry); err != nil {
		return enrollmentCSRStatusCacheEntry{}, false, fmt.Errorf("decoding cached enrollment CSR status: %w", err)
	}
	return entry, true, nil
}

func (c *RedisEnrollmentCSRStatusCache) Set(ctx context.Context, orgId uuid.UUID, certificateDigest string, entry enrollmentCSRStatusCacheEntry) error {
	value, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encoding enrollment CSR status for cache: %w", err)
	}
	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	if err := c.kvStore.Set(ctx, key, value, c.ttl); err != nil {
		return fmt.Errorf("setting cached enrollment CSR status: %w", err)
	}
	return nil
}

func (c *RedisEnrollmentCSRStatusCache) SetMissing(ctx context.Context, orgId uuid.UUID, certificateDigest string) error {
	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	if err := c.kvStore.Set(ctx, key, []byte(missingCSRStatusValue), c.ttl); err != nil {
		return fmt.Errorf("setting cached missing enrollment CSR status: %w", err)
	}
	return nil
}
