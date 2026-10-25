package middleware

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
)

const DefaultEnrollmentCSRStatusCacheTTL = 30 * time.Second

type RedisEnrollmentCSRStatusCache struct {
	kvStore kvstore.KVStore
	ttl     time.Duration
}

func NewRedisEnrollmentCSRStatusCache(kvStore kvstore.KVStore, ttl time.Duration) *RedisEnrollmentCSRStatusCache {
	if ttl <= 0 {
		ttl = DefaultEnrollmentCSRStatusCacheTTL
	}
	return &RedisEnrollmentCSRStatusCache{kvStore: kvStore, ttl: ttl}
}

func (c *RedisEnrollmentCSRStatusCache) Get(ctx context.Context, orgId uuid.UUID, certificateDigest string) (domain.CertificateSigningRequestStatus, bool, error) {
	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	value, err := c.kvStore.Get(ctx, key)
	if err != nil {
		return domain.CertificateSigningRequestStatus{}, false, err
	}
	if len(value) == 0 {
		return domain.CertificateSigningRequestStatus{}, false, nil
	}

	var status domain.CertificateSigningRequestStatus
	if err := json.Unmarshal(value, &status); err != nil {
		return domain.CertificateSigningRequestStatus{}, false, err
	}
	return status, true, nil
}

func (c *RedisEnrollmentCSRStatusCache) Set(ctx context.Context, orgId uuid.UUID, certificateDigest string, status domain.CertificateSigningRequestStatus) error {
	value, err := json.Marshal(status)
	if err != nil {
		return err
	}
	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	return c.kvStore.Set(ctx, key, value, c.ttl)
}
