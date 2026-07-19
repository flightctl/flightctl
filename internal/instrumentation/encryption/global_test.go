package encryption

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestKey writes a random AES-256 key to a temp file and returns a Config.
func setupTestKey(t *testing.T) *config.Config {
	t.Helper()
	key, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	return writeTestKey(t, key, "default")
}

// writeTestKey writes a key string to a temp file and returns a Config.
func writeTestKey(t *testing.T, key, keyID string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyPath, []byte(key), 0600))
	return &config.Config{
		Encryption: &config.EncryptionConfig{
			Keys:        []config.EncryptionKeyConfig{{ID: keyID, Path: keyPath}},
			ActiveKeyID: keyID,
		},
	}
}

func TestInitGlobalEncryption_WithoutCanaryStore(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	err := InitGlobalEncryption(logger, encCfg)
	require.NoError(t, err)

	// Manager initialized, canary disabled
	assert.NotNil(t, GlobalManager())
	assert.Nil(t, GlobalCanaryManager())
}

func TestInitGlobalEncryption_WithCanaryStore(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canary store
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, encCfg, store)
	require.NoError(t, err)

	// Both managers initialized
	assert.NotNil(t, GlobalManager())
	assert.NotNil(t, GlobalCanaryManager())
}

func TestGlobalCanaryManager_ReturnsNilIfNotInitialized(t *testing.T) {
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil

	canaryMgr := GlobalCanaryManager()
	assert.Nil(t, canaryMgr, "Should return nil if not initialized")
}

func TestGlobalCanaryManager_ReturnsNilIfDisabled(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITHOUT canary store
	err := InitGlobalEncryption(logger, encCfg)
	require.NoError(t, err)

	canaryMgr := GlobalCanaryManager()
	assert.Nil(t, canaryMgr, "Should return nil if canaries disabled")
}

func TestGlobalManager_ReturnsNilIfNotInitialized(t *testing.T) {
	// Reset global state
	globalManager = nil

	mgr := GlobalManager()
	assert.Nil(t, mgr, "Should return nil if not initialized")
}

func TestEncrypt_CreatesCanaryOnFirstUse(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canary store
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, encCfg, store)
	require.NoError(t, err)

	ctx := context.Background()

	// Before first encryption, no canary should exist
	canaryMgr := GlobalCanaryManager()
	activeCanary, err := canaryMgr.GetActiveCanary(ctx)
	require.NoError(t, err)
	assert.Nil(t, activeCanary, "No canary should exist before first encryption")

	// First encryption should create canary
	plaintext := Plaintext([]byte("test-data"))
	ciphertext, err := Encrypt(ctx, plaintext)
	require.NoError(t, err)
	assert.NotEmpty(t, ciphertext)

	// Now canary should exist
	activeCanary, err = canaryMgr.GetActiveCanary(ctx)
	require.NoError(t, err)
	assert.NotNil(t, activeCanary, "Canary should exist after first encryption")
	assert.Equal(t, "v1", activeCanary.Strategy)
	assert.Equal(t, "default", activeCanary.KeyID)
}

func TestEncrypt_CanaryDoOnce(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canary store
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, encCfg, store)
	require.NoError(t, err)

	ctx := context.Background()

	// First encryption
	_, err = Encrypt(ctx, Plaintext([]byte("data1")))
	require.NoError(t, err)

	canaryMgr := GlobalCanaryManager()
	canary1, _ := canaryMgr.GetActiveCanary(ctx)

	// Second encryption
	_, err = Encrypt(ctx, Plaintext([]byte("data2")))
	require.NoError(t, err)

	canary2, _ := canaryMgr.GetActiveCanary(ctx)

	// Canary should be identical (do-once, not recreated)
	assert.Equal(t, canary1.EncryptedValue, canary2.EncryptedValue)
	assert.Equal(t, canary1.CreatedAt, canary2.CreatedAt)
}

func TestInitGlobalEncryption_ValidatesExistingCanaries(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canary store
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, encCfg, store)
	require.NoError(t, err)

	// Create a canary
	ctx := context.Background()
	_, err = Encrypt(ctx, Plaintext([]byte("test")))
	require.NoError(t, err)

	// Verify canary was created
	canaryMgr := GlobalCanaryManager()
	allCanaries, _ := canaryMgr.store.GetAll()
	assert.Len(t, allCanaries, 1, "Should have one canary")

	// Reset and re-init (simulating restart)
	// Re-init with the SAME store to test validation of existing canaries
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	// Re-init should validate the existing canary in the store
	err = InitGlobalEncryptionWithCanary(logger, encCfg, store)
	require.NoError(t, err, "Should successfully validate existing canary on restart")

	// Verify validation actually happened and succeeded
	results, err := GlobalCanaryManager().ValidateAll(ctx)
	require.NoError(t, err)
	assert.Len(t, results, 1, "Should have validated one canary")
	assert.Equal(t, "ok", results[0].Status, "Canary validation should succeed")
}

func TestInitGlobalEncryption_ActiveKeyCannotDecryptCanary_FailsInit(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Init WITH canary store
	store := newMemoryCanaryStore()
	err := InitGlobalEncryptionWithCanary(logger, encCfg, store)
	require.NoError(t, err)

	// Create a canary with the current key
	ctx := context.Background()
	_, err = Encrypt(ctx, Plaintext([]byte("test")))
	require.NoError(t, err)

	// Verify canary exists
	canaries, _ := store.GetAll()
	require.Len(t, canaries, 1)

	// Reset and re-init with a DIFFERENT key (simulating key rotation gone wrong)
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	// Generate a different key
	newKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	newEncCfg := writeTestKey(t, newKey, "default")

	// Re-init should FAIL because active key cannot decrypt existing canary
	err = InitGlobalEncryptionWithCanary(logger, newEncCfg, store)
	require.Error(t, err, "Should fail when active key cannot decrypt canary")
	assert.Contains(t, err.Error(), "active encryption key v1/default is broken")
}

func TestInitGlobalEncryption_OldKeyCannotDecrypt_WarnsButSucceeds(t *testing.T) {
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Create manager with TWO keys
	key1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key2, err := crypto.GenerateAES256Key()
	require.NoError(t, err)

	encCfg := writeTestKey(t, key1, "default")

	store := newMemoryCanaryStore()
	err = InitGlobalEncryptionWithCanary(logger, encCfg, store)
	require.NoError(t, err)

	// Create canary for the default key (key1)
	ctx := context.Background()
	_, err = Encrypt(ctx, Plaintext([]byte("test")))
	require.NoError(t, err)

	// Add second key manually and create a canary for it
	strategy, exists := GlobalManager().GetStrategy("v1")
	require.True(t, exists)
	v1Strategy := strategy.(*V1Strategy)
	key2Bytes, _ := crypto.DecodeAES256Key(key2)
	_ = v1Strategy.AddKey("oldkey", key2Bytes, true)

	canaryMgr := GlobalCanaryManager()
	_ = canaryMgr.EnsureCanary(ctx, "v1", "oldkey")

	// Verify we have two canaries now
	canaries, _ := store.GetAll()
	require.Len(t, canaries, 2)

	// Reset and re-init with only key1 (oldkey is missing)
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	// Re-init should SUCCEED (only old key is broken, not active key)
	err = InitGlobalEncryptionWithCanary(logger, encCfg, store)
	require.NoError(t, err, "Should succeed even when old key cannot decrypt")

	// Verify: active key's canary is ok, old key's canary failed
	results, err := GlobalCanaryManager().ValidateAll(ctx)
	require.NoError(t, err)
	require.Len(t, results, 2)

	// Find results by keyID
	var defaultResult, oldkeyResult *ValidationResult
	for i := range results {
		switch results[i].KeyID {
		case "default":
			defaultResult = &results[i]
		case "oldkey":
			oldkeyResult = &results[i]
		}
	}

	assert.NotNil(t, defaultResult)
	assert.NotNil(t, oldkeyResult)
	assert.Equal(t, "ok", defaultResult.Status, "Active key should validate ok")
	assert.Equal(t, "failed", oldkeyResult.Status, "Old key should fail validation")
}

func TestDecrypt_EncryptedData(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	err := InitGlobalEncryption(logger, encCfg)
	require.NoError(t, err)

	ctx := context.Background()

	// Encrypt some data
	plaintext := Plaintext([]byte("secret-data"))
	ciphertext, err := Encrypt(ctx, plaintext)
	require.NoError(t, err)

	// Decrypt should return (plaintext, true, nil)
	decrypted, wasEncrypted, err := Decrypt(ctx, ciphertext)
	require.NoError(t, err)
	assert.True(t, wasEncrypted, "Should indicate data was encrypted")
	assert.Equal(t, plaintext, decrypted, "Decrypted should match original plaintext")
}

func TestDecrypt_PlaintextData_BackwardCompatibility(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	err := InitGlobalEncryption(logger, encCfg)
	require.NoError(t, err)

	ctx := context.Background()

	// Pass plaintext data (no "enc:" prefix)
	plaintext := Ciphertext([]byte("not-encrypted-data"))

	// Decrypt should return (data, false, nil) - backward compatibility
	decrypted, wasEncrypted, err := Decrypt(ctx, plaintext)
	require.NoError(t, err)
	assert.False(t, wasEncrypted, "Should indicate data was NOT encrypted")
	assert.Equal(t, Plaintext(plaintext), decrypted, "Should return input unchanged")
}

func TestDecrypt_NotInitialized(t *testing.T) {
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil

	ctx := context.Background()
	ciphertext := Ciphertext([]byte("enc:v1:default:abc123"))

	// Decrypt should fail with "not initialized" error
	_, _, err := Decrypt(ctx, ciphertext)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encryption not initialized")
}

func TestDecrypt_InvalidFormat(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	err := InitGlobalEncryption(logger, encCfg)
	require.NoError(t, err)

	ctx := context.Background()

	// Invalid encrypted format
	ciphertext := Ciphertext([]byte("enc:v1:default:INVALID-BASE64!!!"))

	// Decrypt should fail
	_, _, err = Decrypt(ctx, ciphertext)
	require.Error(t, err)
}

func TestInitGlobalEncryption_CachesErrorAcrossCalls(t *testing.T) {
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	// Config with nonexistent key path - init should fail
	badCfg := &config.Config{
		Encryption: &config.EncryptionConfig{
			Keys:        []config.EncryptionKeyConfig{{ID: "default", Path: filepath.Join(t.TempDir(), "missing-key")}},
			ActiveKeyID: "default",
		},
	}

	// First call - should fail
	err1 := InitGlobalEncryption(logger, badCfg)
	require.Error(t, err1, "First init should fail when key is missing")

	// Second call - should return the SAME error, not nil
	err2 := InitGlobalEncryption(logger, badCfg)
	require.Error(t, err2, "Second init should also fail with cached error")
	assert.Equal(t, err1.Error(), err2.Error(), "Both calls should return same error")

	// Manager should still be nil
	assert.Nil(t, GlobalManager(), "Manager should remain nil after failed init")
}

func TestKeyRotation_AddNewKeyAndReencrypt(t *testing.T) {
	encCfg := setupTestKey(t)
	// Reset global state
	globalManager = nil
	globalCanaryManager = nil
	globalLogger = nil
	globalManagerOnce = *new(sync.Once)
	globalInitErr = nil

	logger := logrus.New()
	logger.SetLevel(logrus.FatalLevel)

	err := InitGlobalEncryption(logger, encCfg)
	require.NoError(t, err)

	ctx := context.Background()
	mgr := GlobalManager()

	// Encrypt data with the original key
	plaintext := []byte("rotate-me-secret")
	ciphertext, err := Encrypt(ctx, Plaintext(plaintext))
	require.NoError(t, err)
	assert.Contains(t, string(ciphertext), "enc:v1:default:")

	// Add a new key and make it active (simulating key rotation)
	_, v1Strat := mgr.GetActiveStrategy()
	v1 := v1Strat.(*V1Strategy)

	newKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	newKeyBytes, err := crypto.DecodeAES256Key(newKey)
	require.NoError(t, err)
	require.NoError(t, v1.AddKey("2025-07", newKeyBytes, true))

	assert.Equal(t, "2025-07", v1.ActiveKeyID())

	// Old ciphertext still decrypts (old key is retained)
	decrypted, wasEncrypted, err := Decrypt(ctx, ciphertext)
	require.NoError(t, err)
	assert.True(t, wasEncrypted)
	assert.Equal(t, plaintext, decrypted.Bytes())

	// ProcessEncryption re-encrypts with the new active key
	reencrypted, err := mgr.ProcessEncryption(ctx, ciphertext.Bytes())
	require.NoError(t, err)
	assert.Contains(t, string(reencrypted), "enc:v1:2025-07:", "Should be re-encrypted with new key")
	assert.NotEqual(t, ciphertext.Bytes(), reencrypted, "Re-encrypted ciphertext should differ")

	// Re-encrypted data decrypts to the same plaintext
	decrypted2, wasEncrypted2, err := Decrypt(ctx, Ciphertext(reencrypted))
	require.NoError(t, err)
	assert.True(t, wasEncrypted2)
	assert.Equal(t, plaintext, decrypted2.Bytes())

	// ProcessEncryption on already-rotated data is idempotent
	reencrypted2, err := mgr.ProcessEncryption(ctx, reencrypted)
	require.NoError(t, err)
	assert.Equal(t, reencrypted, reencrypted2, "Same key should not re-encrypt again")

	// New encryptions use the new key
	newCiphertext, err := Encrypt(ctx, Plaintext([]byte("new-secret")))
	require.NoError(t, err)
	assert.Contains(t, string(newCiphertext), "enc:v1:2025-07:")
}
