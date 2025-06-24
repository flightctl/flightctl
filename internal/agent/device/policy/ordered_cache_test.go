package policy

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOrderedCache_BasicOperations(t *testing.T) {
	cache := NewOrderedCache[string, int]()

	// Test Put and Get
	cache.Put("key1", 10)
	cache.Put("key2", 20)
	cache.Put("key3", 30)

	val, exists := cache.Get("key1")
	require.True(t, exists)
	require.Equal(t, 10, val)

	val, exists = cache.Get("key2")
	require.True(t, exists)
	require.Equal(t, 20, val)

	val, exists = cache.Get("key3")
	require.True(t, exists)
	require.Equal(t, 30, val)

	// Test non-existent key
	_, exists = cache.Get("nonexistent")
	require.False(t, exists)

	// Test size
	require.Equal(t, 3, cache.Size())
}

func TestOrderedCache_Ordering(t *testing.T) {
	cache := NewOrderedCache[string, int]()

	// Add items in order
	cache.Put("key1", 10)
	cache.Put("key2", 20)
	cache.Put("key3", 30)
	cache.Put("key4", 40)
	cache.Put("key5", 50)

	// Keys should be ordered from oldest to newest
	keys := cache.Keys()
	require.Equal(t, []string{"key1", "key2", "key3", "key4", "key5"}, keys)
}

func TestOrderedCache_UpdateExisting(t *testing.T) {
	cache := NewOrderedCache[string, int]()

	cache.Put("key1", 10)
	cache.Put("key2", 20)

	// Update existing key
	cache.Put("key1", 100)

	val, exists := cache.Get("key1")
	require.True(t, exists)
	require.Equal(t, 100, val)

	// Should still have only 2 items
	require.Equal(t, 2, cache.Size())
}

func TestOrderedCache_Has(t *testing.T) {
	cache := NewOrderedCache[string, int]()

	cache.Put("key1", 10)
	require.True(t, cache.Has("key1"))

	cache.Put("key2", 20)
	cache.Put("key3", 30)

	require.True(t, cache.Has("key1"))
	require.True(t, cache.Has("key2"))
	require.True(t, cache.Has("key3"))
	require.False(t, cache.Has("nonexistent"))
}

func TestOrderedCache_Remove(t *testing.T) {
	cache := NewOrderedCache[string, int]()

	cache.Put("key1", 10)
	cache.Put("key2", 20)

	// Remove existing key
	removed := cache.Remove("key1")
	require.True(t, removed)
	require.Equal(t, 1, cache.Size())

	// Try to remove non-existent key
	removed = cache.Remove("nonexistent")
	require.False(t, removed)

	// Verify key1 is gone
	_, exists := cache.Get("key1")
	require.False(t, exists)
}

func TestOrderedCache_Clear(t *testing.T) {
	cache := NewOrderedCache[string, int]()

	cache.Put("key1", 10)
	cache.Put("key2", 20)
	cache.Put("key3", 30)

	cache.Clear()
	require.Equal(t, 0, cache.Size())

	_, exists := cache.Get("key1")
	require.False(t, exists)
}

func TestOrderedCache_CompactUpTo(t *testing.T) {
	cache := NewOrderedCache[string, int]()

	// Add keys in order
	cache.Put("1", 10)
	cache.Put("2", 20)
	cache.Put("3", 30)
	cache.Put("4", 40)
	cache.Put("5", 50)

	require.Equal(t, 5, cache.Size())
	keys := cache.Keys()
	require.Equal(t, []string{"1", "2", "3", "4", "5"}, keys)

	cache.CompactUpTo("3")

	require.Equal(t, 3, cache.Size())
	keys = cache.Keys()
	require.Equal(t, []string{"3", "4", "5"}, keys)

	// Verify "1" and "2" are gone
	_, exists := cache.Get("1")
	require.False(t, exists)
	_, exists = cache.Get("2")
	require.False(t, exists)

	// Verify "3", "4", "5" still exist
	val, exists := cache.Get("3")
	require.True(t, exists)
	require.Equal(t, 30, val)
	val, exists = cache.Get("4")
	require.True(t, exists)
	require.Equal(t, 40, val)
	val, exists = cache.Get("5")
	require.True(t, exists)
	require.Equal(t, 50, val)
}

func TestOrderedCache_Keys(t *testing.T) {
	cache := NewOrderedCache[string, int]()

	cache.Put("key3", 30)
	cache.Put("key1", 10)
	cache.Put("key2", 20)

	keys := cache.Keys()
	require.Len(t, keys, 3)

	// Keys should be in insertion order (oldest to newest)
	require.Equal(t, []string{"key3", "key1", "key2"}, keys)
}

func TestOrderedCache_GenericTypes(t *testing.T) {
	// Test with different types
	stringCache := NewOrderedCache[int, string]()
	stringCache.Put(1, "one")
	stringCache.Put(2, "two")

	val, exists := stringCache.Get(1)
	require.True(t, exists)
	require.Equal(t, "one", val)

	// Test with custom struct
	type testStruct struct {
		Name string
		Age  int
	}

	structCache := NewOrderedCache[string, *testStruct]()
	structCache.Put("person1", &testStruct{Name: "Alice", Age: 30})

	person, exists := structCache.Get("person1")
	require.True(t, exists)
	require.Equal(t, "Alice", person.Name)
	require.Equal(t, 30, person.Age)
}

func TestOrderedCache_VersionSchedules(t *testing.T) {
	cache := NewOrderedCache[string, *versionSchedules]()

	vs1 := &versionSchedules{}
	vs2 := &versionSchedules{}

	cache.Put("v1", vs1)
	cache.Put("v2", vs2)

	retrieved, exists := cache.Get("v1")
	require.True(t, exists)
	require.Equal(t, vs1, retrieved)

	// Keys should show insertion order
	keys := cache.Keys()
	require.Equal(t, []string{"v1", "v2"}, keys)
}

func TestOrderedCache_ConcurrentAccess(t *testing.T) {
	cache := NewOrderedCache[int, string]()
	numGoroutines := 10
	numOperations := 100

	// Test concurrent Put operations
	t.Run("concurrent puts", func(t *testing.T) {
		var wg sync.WaitGroup

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(goroutineID int) {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					key := goroutineID*numOperations + j
					value := fmt.Sprintf("value-%d-%d", goroutineID, j)
					cache.Put(key, value)
				}
			}(i)
		}

		wg.Wait()

		// Verify some items were added
		require.True(t, cache.Size() > 0)
	})

	// Test concurrent Get operations
	t.Run("concurrent gets", func(t *testing.T) {
		// First, populate the cache
		for i := 0; i < 50; i++ {
			cache.Put(i, fmt.Sprintf("value-%d", i))
		}

		var wg sync.WaitGroup
		successCount := int64(0)

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					key := j % 50 // Access keys that should exist
					if _, exists := cache.Get(key); exists {
						atomic.AddInt64(&successCount, 1)
					}
				}
			}()
		}

		wg.Wait()

		// Should have many successful gets
		require.True(t, successCount > 0)
	})

	// Test mixed concurrent operations
	t.Run("mixed operations", func(t *testing.T) {
		cache := NewOrderedCache[int, string]()
		var wg sync.WaitGroup

		// Writers
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(writerID int) {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					key := writerID*100 + j
					cache.Put(key, fmt.Sprintf("writer-%d-value-%d", writerID, j))
				}
			}(i)
		}

		// Readers
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					key := j % 300 // Try to read various keys
					cache.Get(key)
				}
			}()
		}

		// Mixed operations (Has, Size, Keys)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				cache.Has(j)
				cache.Size()
				if j%10 == 0 {
					cache.Keys() // More expensive operation
				}
			}
		}()

		wg.Wait()

		// Verify cache is in a consistent state
		keys := cache.Keys()
		require.Equal(t, cache.Size(), len(keys))
	})
}

func TestOrderedCache_ConcurrentCompaction(t *testing.T) {
	cache := NewOrderedCache[int, string]()
	numGoroutines := 5
	numOperations := 50

	var wg sync.WaitGroup

	// Writers adding keys
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := goroutineID*numOperations + j
				value := fmt.Sprintf("value-%d", key)
				cache.Put(key, value)
			}
		}(i)
	}

	// Concurrent compaction
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond) // Let some items be added first
		for i := 0; i < 10; i++ {
			compactKey := i * 20
			cache.CompactUpTo(compactKey)
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()

	// Should be in a consistent state
	require.True(t, cache.Size() >= 0)
	keys := cache.Keys()
	require.Equal(t, cache.Size(), len(keys))
}

func TestOrderedCache_RaceConditionDetection(t *testing.T) {
	// This test is designed to catch race conditions when run with -race flag
	cache := NewOrderedCache[int, int]()

	var wg sync.WaitGroup
	numGoroutines := 10
	numOperations := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := j % 20 // Overlap keys to increase contention

				// Mix of operations
				switch j % 5 {
				case 0:
					cache.Put(key, goroutineID*1000+j)
				case 1:
					cache.Get(key)
				case 2:
					cache.Has(key)
				case 3:
					cache.Remove(key)
				case 4:
					cache.CompactUpTo(key)
				}
			}
		}(i)
	}

	wg.Wait()

	// Final consistency check
	size := cache.Size()
	keys := cache.Keys()
	require.Equal(t, size, len(keys))
}
