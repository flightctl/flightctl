package cache

import (
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
)

const DefaultTTL = 10 * time.Minute

type OrganizationCache interface {
	Get(id uuid.UUID) *model.Organization
	Set(id uuid.UUID, org *model.Organization)
}

type OrganizationTTLCache struct {
	cache *ttlcache.Cache[uuid.UUID, *model.Organization]
}

func NewOrganizationTTL(ttl time.Duration) *OrganizationTTLCache {
	opts := []ttlcache.Option[uuid.UUID, *model.Organization]{}
	opts = append(opts, ttlcache.WithTTL[uuid.UUID, *model.Organization](ttl))

	return &OrganizationTTLCache{
		cache: ttlcache.New(opts...),
	}
}

func (c *OrganizationTTLCache) Get(id uuid.UUID) *model.Organization {
	if item := c.cache.Get(id); item != nil {
		return item.Value()
	}
	return nil
}

func (c *OrganizationTTLCache) Set(id uuid.UUID, org *model.Organization) {
	c.cache.Set(id, org, ttlcache.DefaultTTL)
}

func (c *OrganizationTTLCache) Start() {
	c.cache.Start()
}

func (c *OrganizationTTLCache) Stop() {
	c.cache.Stop()
}
