package middleware

import (
	"context"
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
}

func (f *fakeEnrollmentStatusKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	return f.values[key], nil
}

func (f *fakeEnrollmentStatusKVStore) Set(ctx context.Context, key string, value []byte, expiration time.Duration) error {
	f.values[key] = value
	f.ttls[key] = expiration
	return nil
}

func TestRedisEnrollmentCSRStatusCacheWhenStatusIsStoredItShouldRoundTripWithTTL(t *testing.T) {
	orgId := uuid.New()
	certificateDigest := "certificate-digest"
	kv := &fakeEnrollmentStatusKVStore{
		values: map[string][]byte{},
		ttls:   map[string]time.Duration{},
	}
	cache := NewRedisEnrollmentCSRStatusCache(kv, time.Minute)
	status := domain.CertificateSigningRequestStatus{
		Conditions: []domain.Condition{{
			Type:   domain.ConditionTypeCertificateSigningRequestApproved,
			Status: domain.ConditionStatusTrue,
		}},
	}

	require.NoError(t, cache.Set(context.Background(), orgId, certificateDigest, status))

	cached, found, err := cache.Get(context.Background(), orgId, certificateDigest)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, status, cached)

	key := (&kvstore.EnrollmentCSRStatusKey{OrgID: orgId, CertificateDigest: certificateDigest}).ComposeKey()
	require.Equal(t, time.Minute, kv.ttls[key])
}
