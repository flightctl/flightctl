package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
)

// OrgResolver validates organization IDs with caching to minimize database hits.
// It is safe for concurrent use by multiple goroutines.
type OrgResolver struct {
	store Organization
	cache *ttlcache.Cache[uuid.UUID, bool]
}

// NewOrgResolver constructs a new resolver. A TTL of zero disables expiration.
func NewOrgResolver(s Organization, ttl time.Duration) *OrgResolver {
	opts := []ttlcache.Option[uuid.UUID, bool]{}
	if ttl > 0 {
		opts = append(opts, ttlcache.WithTTL[uuid.UUID, bool](ttl))
	}
	c := ttlcache.New(opts...)
	go c.Start()
	return &OrgResolver{store: s, cache: c}
}

// Validate checks that the given organization ID exists. It caches positive
// look-ups according to the configured TTL.
func (r *OrgResolver) Validate(ctx context.Context, id uuid.UUID) error {
	if item := r.cache.Get(id); item != nil {
		return nil
	}
	// Cache miss – query the store.
	if _, err := r.store.GetByID(ctx, id); err != nil {
		return err
	}
	r.cache.Set(id, true, ttlcache.DefaultTTL)
	return nil
}
