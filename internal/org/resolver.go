package org

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
)

// OrgLookup retrieves an organization by ID.
type OrgLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Organization, error)
}

// Resolver caches organization ID validation.
type Resolver struct {
	store OrgLookup
	cache *ttlcache.Cache[uuid.UUID, bool]
	ttl   time.Duration
}

// NewResolver constructs a new resolver. A TTL of zero disables expiration.
func NewResolver(s OrgLookup, ttl time.Duration) *Resolver {
	opts := []ttlcache.Option[uuid.UUID, bool]{}
	if ttl > 0 {
		opts = append(opts, ttlcache.WithTTL[uuid.UUID, bool](ttl))
	}
	c := ttlcache.New(opts...)
	go c.Start()
	return &Resolver{store: s, cache: c, ttl: ttl}
}

// Validate checks that the given organization ID exists. It caches positive
// look-ups according to the configured TTL. Failed validations are not cached,
// ensuring that newly created organizations are immediately accessible.
func (r *Resolver) Validate(ctx context.Context, id uuid.UUID) error {
	if item := r.cache.Get(id); item != nil {
		return nil
	}
	// Cache miss – query the store.
	if _, err := r.store.GetByID(ctx, id); err != nil {
		return err
	}
	// Use configured TTL or disable expiration if TTL is not positive
	cacheTTL := ttlcache.NoTTL
	if r.ttl > 0 {
		cacheTTL = r.ttl
	}
	r.cache.Set(id, true, cacheTTL)
	return nil
}

// Close stops the cache and releases resources. Should be called when the resolver
// is no longer needed to prevent goroutine leaks.
func (r *Resolver) Close() {
	r.cache.Stop()
}
