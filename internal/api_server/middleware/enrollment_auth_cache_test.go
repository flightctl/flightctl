package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeEnrollmentStatusKVStore struct {
	kvstore.KVStore
	values map[string][]byte
	ttls   map[string]time.Duration
	gets   int
	sets   int
	getErr error
	setErr error
}

func (f *fakeEnrollmentStatusKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.values[key], nil
}

func (f *fakeEnrollmentStatusKVStore) Set(ctx context.Context, key string, value []byte, expiration time.Duration) error {
	f.sets++
	if f.setErr != nil {
		return f.setErr
	}
	f.values[key] = value
	f.ttls[key] = expiration
	return nil
}

func TestNewRedisEnrollmentCSRStatusCacheWhenKVStoreIsMissingItShouldReturnError(t *testing.T) {
	cache, err := NewRedisEnrollmentCSRStatusCache(nil, time.Minute)

	require.Nil(t, cache)
	require.ErrorContains(t, err, "kvStore is required")
}

func newTestEnrollmentCSRStatusCache() (*RedisEnrollmentCSRStatusCache, *fakeEnrollmentStatusKVStore) {
	kvStore := &fakeEnrollmentStatusKVStore{
		values: map[string][]byte{},
		ttls:   map[string]time.Duration{},
	}
	cache, err := NewRedisEnrollmentCSRStatusCache(kvStore, time.Minute)
	if err != nil {
		panic(err)
	}
	return cache, kvStore
}

func TestRedisEnrollmentCSRStatusCacheWhenNameAndStatusAreStoredItShouldRoundTripWithTTL(t *testing.T) {
	orgId := uuid.New()
	certificateDigest := "certificate-digest"
	kv := &fakeEnrollmentStatusKVStore{
		values: map[string][]byte{},
		ttls:   map[string]time.Duration{},
	}
	cache, err := NewRedisEnrollmentCSRStatusCache(kv, time.Minute)
	require.NoError(t, err)
	name := "enrollment-credential"
	status := domain.CertificateSigningRequestStatus{
		Conditions: []domain.Condition{{
			Type:   domain.ConditionTypeCertificateSigningRequestApproved,
			Status: domain.ConditionStatusTrue,
		}},
	}
	entry := enrollmentCSRStatusCacheEntry{Name: &name, Status: &status}

	require.NoError(t, cache.Set(context.Background(), orgId, certificateDigest, entry))

	cached, found, err := cache.Get(context.Background(), orgId, certificateDigest)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, entry, cached)

	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	require.Equal(t, time.Minute, kv.ttls[key])
}

func TestRedisEnrollmentCSRStatusCacheWhenStatusIsMissingItShouldRoundTripWithTTL(t *testing.T) {
	orgId := uuid.New()
	certificateDigest := "certificate-digest"
	kv := &fakeEnrollmentStatusKVStore{
		values: map[string][]byte{},
		ttls:   map[string]time.Duration{},
	}
	cache, err := NewRedisEnrollmentCSRStatusCache(kv, time.Minute)
	require.NoError(t, err)

	require.NoError(t, cache.SetMissing(context.Background(), orgId, certificateDigest))

	cached, found, err := cache.Get(context.Background(), orgId, certificateDigest)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, enrollmentCSRStatusCacheEntry{}, cached)

	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	require.Equal(t, []byte("null"), kv.values[key])
	require.Equal(t, time.Minute, kv.ttls[key])
}

func TestRedisEnrollmentCSRStatusCacheWhenPayloadIsMalformedItShouldReturnError(t *testing.T) {
	orgId := uuid.New()
	certificateDigest := "certificate-digest"
	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	kv := &fakeEnrollmentStatusKVStore{
		values: map[string][]byte{key: []byte("{")},
		ttls:   map[string]time.Duration{},
	}
	cache, err := NewRedisEnrollmentCSRStatusCache(kv, time.Minute)
	require.NoError(t, err)

	cached, found, err := cache.Get(context.Background(), orgId, certificateDigest)
	require.Error(t, err)
	require.False(t, found)
	require.Equal(t, enrollmentCSRStatusCacheEntry{}, cached)
}

func TestRedisEnrollmentCSRStatusCacheWhenKVStoreFailsItShouldReturnError(t *testing.T) {
	orgId := uuid.New()
	certificateDigest := "certificate-digest"
	getKV := &fakeEnrollmentStatusKVStore{getErr: errors.New("get failed")}
	setKV := &fakeEnrollmentStatusKVStore{setErr: errors.New("set failed")}
	getCache, err := NewRedisEnrollmentCSRStatusCache(getKV, time.Minute)
	require.NoError(t, err)
	setCache, err := NewRedisEnrollmentCSRStatusCache(setKV, time.Minute)
	require.NoError(t, err)

	_, _, err = getCache.Get(context.Background(), orgId, certificateDigest)
	require.ErrorContains(t, err, "get failed")
	require.ErrorContains(t, setCache.Set(context.Background(), orgId, certificateDigest, enrollmentCSRStatusCacheEntry{}), "set failed")
}
