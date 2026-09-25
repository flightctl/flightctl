package oci

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type digestCache struct {
	values map[string][]byte
}

func (c *digestCache) Get(_ context.Context, key string) ([]byte, error) {
	return c.values[key], nil
}

func (c *digestCache) SetNX(_ context.Context, key string, value []byte) (bool, error) {
	if _, ok := c.values[key]; ok {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *digestCache) SetExpire(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func TestCachedImageDigestScopesCacheByOrganization(t *testing.T) {
	cache := &digestCache{values: make(map[string][]byte)}
	image := "quay.io/example/os:latest"
	orgA := uuid.New()
	orgB := uuid.New()

	gotA, err := CachedImageDigest(context.Background(), cache, orgA, image, func(context.Context) (string, error) {
		return "sha256:aaa", nil
	})
	require.NoError(t, err)
	require.Equal(t, "sha256:aaa", gotA)

	gotB, err := CachedImageDigest(context.Background(), cache, orgB, image, func(context.Context) (string, error) {
		return "sha256:bbb", nil
	})
	require.NoError(t, err)
	require.Equal(t, "sha256:bbb", gotB)
	require.Len(t, cache.values, 2)
}
