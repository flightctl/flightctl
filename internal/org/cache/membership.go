package cache

import (
	"context"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

type Membership interface {
	Get(key string) (bool, bool)
	Set(key string, value bool)
}

type MembershipTTL struct {
	cache *ttlcache.Cache[string, bool]
}

func NewMembershipTTL(ttl time.Duration) *MembershipTTL {
	opts := []ttlcache.Option[string, bool]{}
	opts = append(opts, ttlcache.WithTTL[string, bool](ttl))

	return &MembershipTTL{
		cache: ttlcache.New(opts...),
	}
}

func (c *MembershipTTL) Get(key string) (bool, bool) {
	if item := c.cache.Get(key); item != nil {
		return true, item.Value()
	}
	return false, false
}

func (c *MembershipTTL) Set(key string, value bool) {
	c.cache.Set(key, value, ttlcache.DefaultTTL)
}

// Start starts the cache background cleanup and blocks until context is cancelled
func (c *MembershipTTL) Start(ctx context.Context) {
	go c.cache.Start()
	<-ctx.Done()
	c.cache.Stop()
}

func (c *MembershipTTL) Stop() {
	c.cache.Stop()
}
