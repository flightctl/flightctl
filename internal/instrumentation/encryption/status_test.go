package encryption

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatus_EncryptionNotInitialized(t *testing.T) {
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil

	status, err := Status(context.Background())
	require.NoError(t, err)

	assert.False(t, status.Enabled)
	assert.Nil(t, status.Active)
	assert.False(t, status.CanaryChecksEnabled)
	assert.Empty(t, status.Keys)
}

func TestStatus_CanariesDisabled(t *testing.T) {
	setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITHOUT canaries
	err := InitGlobalEncryption(logger)
	require.NoError(t, err)

	// Add a second key
	mgr := GlobalManager()
	_, v1Strat := mgr.GetActiveStrategy()
	v1 := v1Strat.(*V1Strategy)
	key2 := make([]byte, 32)
	_, err = rand.Read(key2)
	require.NoError(t, err)
	require.NoError(t, v1.AddKey("oldkey", key2, true))
	require.NoError(t, v1.SetActiveKey("default")) // Back to default

	ctx := context.Background()
	status, err := Status(ctx)
	require.NoError(t, err)

	assert.True(t, status.Enabled)
	assert.False(t, status.CanaryChecksEnabled)

	assert.Equal(t, "v1", status.Active.Strategy)
	assert.Equal(t, "default", status.Active.KeyID)
	assert.Equal(t, "AES-256-GCM", status.Active.Algorithm)

	// Should show both configured keys
	assert.Len(t, status.Keys, 2)

	// Find keys in results
	var defaultKey, oldKey *KeyStatus
	for i := range status.Keys {
		if status.Keys[i].KeyID == "default" {
			defaultKey = &status.Keys[i]
		} else if status.Keys[i].KeyID == "oldkey" {
			oldKey = &status.Keys[i]
		}
	}

	require.NotNil(t, defaultKey)
	require.NotNil(t, oldKey)

	// Check default key
	assert.Equal(t, "v1", defaultKey.Strategy)
	assert.True(t, defaultKey.Configured)
	assert.True(t, defaultKey.Active)
	assert.Equal(t, "not_checked", defaultKey.CanaryStatus)

	// Check old key
	assert.Equal(t, "v1", oldKey.Strategy)
	assert.True(t, oldKey.Configured)
	assert.False(t, oldKey.Active)
	assert.Equal(t, "not_checked", oldKey.CanaryStatus)
}

func TestStatus_CanariesEnabled_BeforeFirstEncryption(t *testing.T) {
	setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canaries
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, store)
	require.NoError(t, err)

	ctx := context.Background()
	status, err := Status(ctx)
	require.NoError(t, err)

	assert.True(t, status.Enabled)
	assert.True(t, status.CanaryChecksEnabled)

	// Should show active key
	assert.Len(t, status.Keys, 1)
	assert.Equal(t, "v1", status.Keys[0].Strategy)
	assert.Equal(t, "default", status.Keys[0].KeyID)
	assert.True(t, status.Keys[0].Configured)
	assert.True(t, status.Keys[0].Active)
	assert.Equal(t, "not_checked", status.Keys[0].CanaryStatus) // No canary yet
}

func TestStatus_CanariesEnabled_AfterFirstEncryption(t *testing.T) {
	setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canaries
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, store)
	require.NoError(t, err)

	ctx := context.Background()

	// First encryption creates canary
	_, err = Encrypt(ctx, Plaintext([]byte("test")))
	require.NoError(t, err)

	status, err := Status(ctx)
	require.NoError(t, err)

	assert.True(t, status.Enabled)
	assert.True(t, status.CanaryChecksEnabled)

	// Should show active key with canary
	assert.Len(t, status.Keys, 1)
	assert.Equal(t, "v1", status.Keys[0].Strategy)
	assert.Equal(t, "default", status.Keys[0].KeyID)
	assert.True(t, status.Keys[0].Configured)
	assert.True(t, status.Keys[0].Active)
	assert.Equal(t, "ok", status.Keys[0].CanaryStatus) // Canary validated
}

func TestStatus_MultipleKeys_WithCanaries(t *testing.T) {
	setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canaries
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, store)
	require.NoError(t, err)

	ctx := context.Background()
	mgr := GlobalManager()
	canaryMgr := GlobalCanaryManager()

	// Create canary for default key
	_, err = Encrypt(ctx, Plaintext([]byte("test")))
	require.NoError(t, err)

	// Add oldkey
	_, v1Strat := mgr.GetActiveStrategy()
	v1 := v1Strat.(*V1Strategy)
	key2 := make([]byte, 32)
	_, err = rand.Read(key2)
	require.NoError(t, err)
	require.NoError(t, v1.AddKey("oldkey", key2, true))

	// Create canary for oldkey
	err = canaryMgr.EnsureCanary(ctx, "v1", "oldkey")
	require.NoError(t, err)

	// Switch back to default
	_ = v1.SetActiveKey("default")

	status, err := Status(ctx)
	require.NoError(t, err)

	assert.True(t, status.Enabled)
	assert.True(t, status.CanaryChecksEnabled)

	// Should show both keys
	assert.Len(t, status.Keys, 2)

	// Find keys
	var defaultKey, oldKey *KeyStatus
	for i := range status.Keys {
		if status.Keys[i].KeyID == "default" {
			defaultKey = &status.Keys[i]
		} else if status.Keys[i].KeyID == "oldkey" {
			oldKey = &status.Keys[i]
		}
	}

	require.NotNil(t, defaultKey)
	require.NotNil(t, oldKey)

	// Check default key
	assert.True(t, defaultKey.Active)
	assert.Equal(t, "ok", defaultKey.CanaryStatus)

	// Check old key
	assert.False(t, oldKey.Active)
	assert.Equal(t, "ok", oldKey.CanaryStatus)
}

func TestStatus_HistoricalCanary_KeyStillConfigured(t *testing.T) {
	setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canaries
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, store)
	require.NoError(t, err)

	ctx := context.Background()
	mgr := GlobalManager()
	canaryMgr := GlobalCanaryManager()

	// Create canary for v1/default
	_, err = Encrypt(ctx, Plaintext([]byte("test")))
	require.NoError(t, err)

	// Register v2 (becomes active)
	v2 := &mockStrategy{
		version:     "v2",
		activeKeyID: "k1",
		encryptFunc: func(ctx context.Context, data []byte) ([]byte, error) {
			return []byte("k1:v2-data"), nil
		},
		parseBodyFunc: func(body []byte) (*ParsedEncrypted, error) {
			return &ParsedEncrypted{KeyID: "k1", Payload: body}, nil
		},
		decryptParsedFunc: func(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error) {
			return []byte("decrypted"), nil
		},
	}
	mgr.RegisterStrategy(v2, true)

	// Create canary for v2/k1
	err = canaryMgr.EnsureCanary(ctx, "v2", "k1")
	require.NoError(t, err)

	status, err := Status(ctx)
	require.NoError(t, err)

	// Should show v2/k1 (active) and v1/default (historical canary)
	assert.Len(t, status.Keys, 2)

	// Active should be v2/k1
	assert.Equal(t, "v2", status.Active.Strategy)
	assert.Equal(t, "k1", status.Active.KeyID)

	// Find v1/default (historical)
	var v1Key *KeyStatus
	for i := range status.Keys {
		if status.Keys[i].Strategy == "v1" {
			v1Key = &status.Keys[i]
		}
	}

	require.NotNil(t, v1Key)
	assert.Equal(t, "default", v1Key.KeyID)
	assert.True(t, v1Key.Configured) // v1 strategy still registered
	assert.False(t, v1Key.Active)
	assert.Equal(t, "ok", v1Key.CanaryStatus)
}

func TestStatus_HistoricalCanary_KeyMissing(t *testing.T) {
	setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canaries
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, store)
	require.NoError(t, err)

	ctx := context.Background()

	// Manually create a canary for a non-existent strategy/key
	// (simulating historical data after key removal)
	canary := &Canary{
		Strategy:       "v2",
		KeyID:          "removed-key",
		EncryptedValue: []byte("old-canary-data"),
	}
	_ = store.Save(canary)

	status, err := Status(ctx)
	require.NoError(t, err)

	// Should show active v1/default AND orphaned v2/removed-key
	assert.Len(t, status.Keys, 2)

	// Find orphaned key
	var orphanKey *KeyStatus
	for i := range status.Keys {
		if status.Keys[i].Strategy == "v2" {
			orphanKey = &status.Keys[i]
		}
	}

	require.NotNil(t, orphanKey)
	assert.Equal(t, "removed-key", orphanKey.KeyID)
	assert.False(t, orphanKey.Configured) // v2 not registered
	assert.False(t, orphanKey.Active)
	assert.Equal(t, "key_missing", orphanKey.CanaryStatus) // KEY MISSING!
}

func TestStatus_DeterministicOrdering(t *testing.T) {
	setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Create manager with multiple keys
	key1, _ := crypto.GenerateAES256Key()
	key2, _ := crypto.GenerateAES256Key()
	key3, _ := crypto.GenerateAES256Key()

	t.Setenv("FLIGHTCTL_ENCRYPTION_KEY", key1)

	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, store)
	require.NoError(t, err)

	// Add more keys in random order
	_, v1Strat := GlobalManager().GetActiveStrategy()
	v1Strategy := v1Strat.(*V1Strategy)
	key2Bytes, _ := crypto.DecodeAES256Key(key2)
	key3Bytes, _ := crypto.DecodeAES256Key(key3)
	require.NoError(t, v1Strategy.AddKey("zebra", key2Bytes, true))
	require.NoError(t, v1Strategy.AddKey("alpha", key3Bytes, true))

	ctx := context.Background()

	// Create canaries for all keys
	_, _ = Encrypt(ctx, Plaintext([]byte("test")))
	canaryMgr := GlobalCanaryManager()
	_ = canaryMgr.EnsureCanary(ctx, "v1", "zebra")
	_ = canaryMgr.EnsureCanary(ctx, "v1", "alpha")

	// Call Status multiple times
	status1, err := Status(ctx)
	require.NoError(t, err)

	status2, err := Status(ctx)
	require.NoError(t, err)

	status3, err := Status(ctx)
	require.NoError(t, err)

	// All should have same order
	require.Len(t, status1.Keys, 3)
	require.Len(t, status2.Keys, 3)
	require.Len(t, status3.Keys, 3)

	// Keys should be sorted by strategy, then keyID
	assert.Equal(t, "v1", status1.Keys[0].Strategy)
	assert.Equal(t, "alpha", status1.Keys[0].KeyID)
	assert.Equal(t, "v1", status1.Keys[1].Strategy)
	assert.Equal(t, "default", status1.Keys[1].KeyID)
	assert.Equal(t, "v1", status1.Keys[2].Strategy)
	assert.Equal(t, "zebra", status1.Keys[2].KeyID)

	// All calls should produce identical ordering
	for i := 0; i < 3; i++ {
		assert.Equal(t, status1.Keys[i].Strategy, status2.Keys[i].Strategy)
		assert.Equal(t, status1.Keys[i].KeyID, status2.Keys[i].KeyID)
		assert.Equal(t, status1.Keys[i].Strategy, status3.Keys[i].Strategy)
		assert.Equal(t, status1.Keys[i].KeyID, status3.Keys[i].KeyID)
	}
}
