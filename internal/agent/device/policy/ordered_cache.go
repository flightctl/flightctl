package policy

import (
	"container/list"
	"sync"
)

// OrderedCacheItem represents an item stored in the ordered cache
type OrderedCacheItem[K comparable, V any] struct {
	key   K
	value V
}

// OrderedCache is a generic thread-safe cache that maintains insertion order
// New entries are added to the back, and entries can be compacted from the front
type OrderedCache[K comparable, V any] struct {
	items map[K]*list.Element
	order *list.List
	mutex sync.RWMutex
}

// NewOrderedCache creates a new thread-safe ordered cache
func NewOrderedCache[K comparable, V any]() *OrderedCache[K, V] {
	return &OrderedCache[K, V]{
		items: make(map[K]*list.Element),
		order: list.New(),
	}
}

// Get retrieves a value from the cache
func (c *OrderedCache[K, V]) Get(key K) (V, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var zero V

	element, exists := c.items[key]
	if !exists {
		return zero, false
	}

	item := element.Value.(*OrderedCacheItem[K, V])
	return item.value, true
}

// Put adds or updates a key-value pair in the cache
// New entries are always added to the back (most recent)
func (c *OrderedCache[K, V]) Put(key K, value V) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if element, exists := c.items[key]; exists {
		// Update existing item
		item := element.Value.(*OrderedCacheItem[K, V])
		item.value = value
		return
	}

	// Create new item
	item := &OrderedCacheItem[K, V]{
		key:   key,
		value: value,
	}

	// Add to back of list (most recent) and map
	element := c.order.PushBack(item)
	c.items[key] = element
}

// Has checks if a key exists in the cache
func (c *OrderedCache[K, V]) Has(key K) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	_, exists := c.items[key]
	return exists
}

// Remove removes a key from the cache
func (c *OrderedCache[K, V]) Remove(key K) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	element, exists := c.items[key]
	if !exists {
		return false
	}

	c.order.Remove(element)
	delete(c.items, key)
	return true
}

// CompactUpTo removes all entries from the front until the specified key is found
// The key itself is not removed - only entries that come before it
func (c *OrderedCache[K, V]) CompactUpTo(targetKey K) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Remove from front until we reach the target key
	for c.order.Len() > 0 {
		element := c.order.Front()
		item := element.Value.(*OrderedCacheItem[K, V])

		// Stop when we reach the target key (don't remove it)
		if item.key == targetKey {
			break
		}

		c.order.Remove(element)
		delete(c.items, item.key)
	}
}

// Size returns the current number of items in the cache
func (c *OrderedCache[K, V]) Size() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return len(c.items)
}

// Clear removes all items from the cache
func (c *OrderedCache[K, V]) Clear() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.items = make(map[K]*list.Element)
	c.order.Init()
}

// Keys returns all keys in the cache (from oldest to newest)
func (c *OrderedCache[K, V]) Keys() []K {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	keys := make([]K, 0, len(c.items))
	for element := c.order.Front(); element != nil; element = element.Next() {
		item := element.Value.(*OrderedCacheItem[K, V])
		keys = append(keys, item.key)
	}
	return keys
}
