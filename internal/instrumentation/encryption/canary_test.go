package encryption

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanaryManager_EnsureCanary_CreatesNew(t *testing.T) {
	mgr := createTestManager(t)
	store := newMemoryCanaryStore()
	canaryMgr := NewCanaryManager(mgr, store)

	ctx := context.Background()

	// Ensure canary for v1/default
	err := canaryMgr.EnsureCanary(ctx, "v1", "default")
	require.NoError(t, err)

	// Verify canary was created
	canary, err := store.Get("v1", "default")
	require.NoError(t, err)
	require.NotNil(t, canary)

	assert.Equal(t, "v1", canary.Strategy)
	assert.Equal(t, "default", canary.KeyID)
	assert.NotEmpty(t, canary.EncryptedValue)
	assert.Contains(t, string(canary.EncryptedValue), "enc:v1:default:")
}

func TestCanaryManager_EnsureCanary_DoOnce(t *testing.T) {
	mgr := createTestManager(t)
	store := newMemoryCanaryStore()
	canaryMgr := NewCanaryManager(mgr, store)

	ctx := context.Background()

	// First call creates canary
	err := canaryMgr.EnsureCanary(ctx, "v1", "default")
	require.NoError(t, err)

	canary1, _ := store.Get("v1", "default")

	// Second call should not recreate (do-once)
	err = canaryMgr.EnsureCanary(ctx, "v1", "default")
	require.NoError(t, err)

	canary2, _ := store.Get("v1", "default")

	// Should be exactly the same (not recreated)
	assert.Equal(t, canary1.EncryptedValue, canary2.EncryptedValue)
	assert.Equal(t, canary1.CreatedAt, canary2.CreatedAt)
}

func TestCanaryManager_EnsureCanary_MultipleKeys(t *testing.T) {
	var err error
	mgr := NewManager()

	key1 := make([]byte, 32)
	_, err = rand.Read(key1)
	require.NoError(t, err)

	key2 := make([]byte, 32)
	_, err = rand.Read(key2)
	require.NoError(t, err)

	v1 := newV1Strategy()
	require.NoError(t, v1.AddKey("keyA", key1, true))
	require.NoError(t, v1.AddKey("keyB", key2, true))
	mgr.RegisterStrategy(v1, true)

	store := newMemoryCanaryStore()
	canaryMgr := NewCanaryManager(mgr, store)

	ctx := context.Background()

	// Ensure canaries for both keys
	err = canaryMgr.EnsureCanary(ctx, "v1", "keyA")
	require.NoError(t, err)

	err = canaryMgr.EnsureCanary(ctx, "v1", "keyB")
	require.NoError(t, err)

	// Verify both exist
	canaryA, err := store.Get("v1", "keyA")
	require.NoError(t, err)
	require.NotNil(t, canaryA)

	canaryB, err := store.Get("v1", "keyB")
	require.NoError(t, err)
	require.NotNil(t, canaryB)

	// Should be different (different keys)
	assert.NotEqual(t, canaryA.EncryptedValue, canaryB.EncryptedValue)
}

func TestCanaryManager_ValidateAll_AllOK(t *testing.T) {
	mgr := createTestManager(t)
	store := newMemoryCanaryStore()
	canaryMgr := NewCanaryManager(mgr, store)

	ctx := context.Background()

	// Create canary
	err := canaryMgr.EnsureCanary(ctx, "v1", "default")
	require.NoError(t, err)

	// Validate all
	results, err := canaryMgr.ValidateAll(ctx)
	require.NoError(t, err)

	require.Len(t, results, 1)
	assert.Equal(t, "v1", results[0].Strategy)
	assert.Equal(t, "default", results[0].KeyID)
	assert.Equal(t, "ok", results[0].Status)
	assert.Empty(t, results[0].Error)
}

func TestCanaryManager_ValidateAll_MultipleCanaries(t *testing.T) {
	var err error
	mgr := NewManager()

	key1 := make([]byte, 32)
	_, err = rand.Read(key1)
	require.NoError(t, err)

	key2 := make([]byte, 32)
	_, err = rand.Read(key2)
	require.NoError(t, err)

	v1 := newV1Strategy()
	require.NoError(t, v1.AddKey("keyA", key1, true))
	require.NoError(t, v1.AddKey("keyB", key2, true))
	mgr.RegisterStrategy(v1, true)

	store := newMemoryCanaryStore()
	canaryMgr := NewCanaryManager(mgr, store)

	ctx := context.Background()

	// Create multiple canaries
	_ = canaryMgr.EnsureCanary(ctx, "v1", "keyA")
	_ = canaryMgr.EnsureCanary(ctx, "v1", "keyB")

	// Validate all
	results, err := canaryMgr.ValidateAll(ctx)
	require.NoError(t, err)

	require.Len(t, results, 2)

	// All should be OK
	for _, result := range results {
		assert.Equal(t, "ok", result.Status)
	}
}

func TestCanaryManager_ValidateAll_DecryptFails(t *testing.T) {
	mgr := createTestManager(t)
	store := newMemoryCanaryStore()
	canaryMgr := NewCanaryManager(mgr, store)

	ctx := context.Background()

	// Manually create broken canary (invalid encrypted value)
	brokenCanary := &Canary{
		Strategy:       "v1",
		KeyID:          "default",
		EncryptedValue: []byte("enc:v1:default:BROKEN-BASE64!!!"),
	}
	_ = store.Save(brokenCanary)

	// Validate should catch the error
	results, err := canaryMgr.ValidateAll(ctx)
	require.NoError(t, err)

	require.Len(t, results, 1)
	assert.Equal(t, "failed", results[0].Status)
	assert.Contains(t, results[0].Error, "decrypt failed")
}

func TestCanaryManager_ValidateAll_PlaintextMismatch(t *testing.T) {
	mgr := createTestManager(t)
	store := newMemoryCanaryStore()

	ctx := context.Background()

	// Create canary with wrong plaintext
	wrongPlaintext := []byte("wrong-canary-value")
	encrypted, err := mgr.Encrypt(ctx, wrongPlaintext)
	require.NoError(t, err)

	wrongCanary := &Canary{
		Strategy:       "v1",
		KeyID:          "default",
		EncryptedValue: encrypted,
	}
	_ = store.Save(wrongCanary)

	canaryMgr := NewCanaryManager(mgr, store)

	// Validate should catch the mismatch
	results, err := canaryMgr.ValidateAll(ctx)
	require.NoError(t, err)

	require.Len(t, results, 1)
	assert.Equal(t, "mismatch", results[0].Status)
	assert.Contains(t, results[0].Error, "expected")
}

func TestCanaryManager_GetActiveCanary(t *testing.T) {
	mgr := createTestManager(t)
	store := newMemoryCanaryStore()
	canaryMgr := NewCanaryManager(mgr, store)

	ctx := context.Background()

	// Create canary for active key
	err := canaryMgr.EnsureCanary(ctx, "v1", "default")
	require.NoError(t, err)

	// Get active canary
	activeCanary, err := canaryMgr.GetActiveCanary(ctx)
	require.NoError(t, err)
	require.NotNil(t, activeCanary)

	assert.Equal(t, "v1", activeCanary.Strategy)
	assert.Equal(t, "default", activeCanary.KeyID)
}

func TestCanaryManager_GetActiveCanary_NotFound(t *testing.T) {
	mgr := createTestManager(t)
	store := newMemoryCanaryStore()
	canaryMgr := NewCanaryManager(mgr, store)

	ctx := context.Background()

	// No canary created yet
	activeCanary, err := canaryMgr.GetActiveCanary(ctx)
	require.NoError(t, err)
	assert.Nil(t, activeCanary)
}

func TestMemoryCanaryStore_GetNotFound(t *testing.T) {
	store := newMemoryCanaryStore()

	canary, err := store.Get("v1", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, canary)
}

func TestMemoryCanaryStore_SaveAndGet(t *testing.T) {
	store := newMemoryCanaryStore()

	canary := &Canary{
		Strategy:       "v1",
		KeyID:          "test-key",
		EncryptedValue: []byte("enc:v1:test-key:abc123"),
	}

	err := store.Save(canary)
	require.NoError(t, err)

	retrieved, err := store.Get("v1", "test-key")
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	assert.Equal(t, canary.Strategy, retrieved.Strategy)
	assert.Equal(t, canary.KeyID, retrieved.KeyID)
	assert.Equal(t, canary.EncryptedValue, retrieved.EncryptedValue)
}

func TestMemoryCanaryStore_SaveUpdates(t *testing.T) {
	store := newMemoryCanaryStore()

	canary1 := &Canary{
		Strategy:       "v1",
		KeyID:          "test-key",
		EncryptedValue: []byte("first"),
	}
	_ = store.Save(canary1)

	// Save again with same key, different value
	canary2 := &Canary{
		Strategy:       "v1",
		KeyID:          "test-key",
		EncryptedValue: []byte("second"),
	}
	_ = store.Save(canary2)

	// Should have updated, not created duplicate
	retrieved, _ := store.Get("v1", "test-key")
	assert.Equal(t, []byte("second"), retrieved.EncryptedValue)

	all, _ := store.GetAll()
	assert.Len(t, all, 1, "Should only have one canary")
}

func TestMemoryCanaryStore_GetAll_Empty(t *testing.T) {
	store := newMemoryCanaryStore()

	all, err := store.GetAll()
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestMemoryCanaryStore_GetAll_Multiple(t *testing.T) {
	store := newMemoryCanaryStore()

	canary1 := &Canary{Strategy: "v1", KeyID: "key1"}
	canary2 := &Canary{Strategy: "v1", KeyID: "key2"}
	canary3 := &Canary{Strategy: "v2", KeyID: "key1"}

	_ = store.Save(canary1)
	_ = store.Save(canary2)
	_ = store.Save(canary3)

	all, err := store.GetAll()
	require.NoError(t, err)
	assert.Len(t, all, 3)
}
