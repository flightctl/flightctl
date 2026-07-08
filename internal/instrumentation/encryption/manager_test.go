package encryption

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_RegisterStrategy(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		expectedInMap string
	}{
		{
			name:          "When version is lowercase it should register as-is",
			version:       "v1",
			expectedInMap: "v1",
		},
		{
			name:          "When version is uppercase it should normalize to lowercase",
			version:       "V1",
			expectedInMap: "v1",
		},
		{
			name:          "When version is mixed case it should normalize to kebab-case",
			version:       "V1Beta",
			expectedInMap: "v1beta",
		},
		{
			name:          "When version is snake_case it should normalize to kebab-case",
			version:       "v1_beta",
			expectedInMap: "v1-beta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewManager()
			strategy := &mockStrategy{version: tt.version}

			mgr.RegisterStrategy(strategy, true)

			_, exists := mgr.GetStrategy(tt.expectedInMap)
			assert.True(t, exists, "strategy should be registered")
			assert.Equal(t, tt.expectedInMap, mgr.ActiveStrategyVersion(), "last registered should be active")
		})
	}
}

func TestManager_RegisterMultipleStrategies(t *testing.T) {
	mgr := NewManager()

	v1 := &mockStrategy{version: "v1"}
	v2 := &mockStrategy{version: "v2"}

	mgr.RegisterStrategy(v1, true)
	assert.Equal(t, "v1", mgr.ActiveStrategyVersion(), "v1 should be active after first register")

	mgr.RegisterStrategy(v2, true)
	assert.Equal(t, "v2", mgr.ActiveStrategyVersion(), "v2 should be active after second register")

	assert.Equal(t, 2, mgr.StrategyCount(), "both strategies should be registered")
}

func TestManager_RegisterStrategy_ExplicitActivation(t *testing.T) {
	mgr := NewManager()
	v1 := &mockStrategy{version: "v1"}
	v2 := &mockStrategy{version: "v2"}

	// Register v1 as active
	mgr.RegisterStrategy(v1, true)
	assert.Equal(t, "v1", mgr.ActiveStrategyVersion(), "v1 should be active when setActive=true")

	// Register v2 but DON'T activate it
	mgr.RegisterStrategy(v2, false)
	assert.Equal(t, "v1", mgr.ActiveStrategyVersion(), "v1 should still be active when v2 registered with setActive=false")

	// Both should be registered
	assert.Equal(t, 2, mgr.StrategyCount(), "both strategies should be registered")
	_, v1Exists := mgr.GetStrategy("v1")
	assert.True(t, v1Exists, "v1 should be registered")
	_, v2Exists := mgr.GetStrategy("v2")
	assert.True(t, v2Exists, "v2 should be registered")

	// Can still explicitly activate v2
	err := mgr.SetActiveStrategy("v2")
	require.NoError(t, err)
	assert.Equal(t, "v2", mgr.ActiveStrategyVersion(), "v2 should be active after SetActiveStrategy")
}

func TestManager_SetActiveStrategy(t *testing.T) {
	mgr := NewManager()
	v1 := &mockStrategy{version: "v1"}
	v2 := &mockStrategy{version: "v2"}

	mgr.RegisterStrategy(v1, true)
	mgr.RegisterStrategy(v2, true)

	// Switch back to v1
	err := mgr.SetActiveStrategy("v1")
	require.NoError(t, err)
	assert.Equal(t, "v1", mgr.ActiveStrategyVersion())

	// Version normalization should work
	err = mgr.SetActiveStrategy("V1")
	require.NoError(t, err)
	assert.Equal(t, "v1", mgr.ActiveStrategyVersion())

	// Unknown version should error
	err = mgr.SetActiveStrategy("v3")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")
}

func TestManager_EncryptDecrypt(t *testing.T) {
	mgr := NewManager()
	strategy := &mockStrategy{
		version: "v1",
		encryptFunc: func(ctx context.Context, data []byte) ([]byte, error) {
			return []byte("encrypted:" + string(data)), nil
		},
		parseBodyFunc: func(body []byte) (*ParsedEncrypted, error) {
			return &ParsedEncrypted{KeyID: "default", Payload: body}, nil
		},
		decryptParsedFunc: func(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error) {
			return []byte(string(parsed.Payload)[10:]), nil // Strip "encrypted:" prefix
		},
	}

	mgr.RegisterStrategy(strategy, true)

	ctx := context.Background()
	plaintext := []byte("my-secret")

	// Encrypt
	encrypted, err := mgr.Encrypt(ctx, plaintext)
	require.NoError(t, err)
	assert.Equal(t, "enc:v1:encrypted:my-secret", string(encrypted))

	// Decrypt
	decrypted, err := mgr.Decrypt(ctx, encrypted)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestManager_EncryptWithoutActiveStrategy(t *testing.T) {
	mgr := NewManager()
	ctx := context.Background()

	_, err := mgr.Encrypt(ctx, []byte("data"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no active encryption strategy")
}

func TestManager_DecryptPlaintextPassthrough(t *testing.T) {
	mgr := NewManager()
	ctx := context.Background()

	// Plaintext without "enc:" prefix should pass through unchanged
	plaintext := []byte("old-plaintext-password")
	decrypted, err := mgr.Decrypt(ctx, plaintext)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestManager_DecryptWithVersionRouting(t *testing.T) {
	mgr := NewManager()

	v1 := &mockStrategy{
		version: "v1",
		parseBodyFunc: func(body []byte) (*ParsedEncrypted, error) {
			return &ParsedEncrypted{KeyID: "default", Payload: body}, nil
		},
		decryptParsedFunc: func(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error) {
			return []byte("decrypted-by-v1"), nil
		},
	}
	v2 := &mockStrategy{
		version: "v2",
		parseBodyFunc: func(body []byte) (*ParsedEncrypted, error) {
			return &ParsedEncrypted{KeyID: "default", Payload: body}, nil
		},
		decryptParsedFunc: func(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error) {
			return []byte("decrypted-by-v2"), nil
		},
	}

	mgr.RegisterStrategy(v1, true)
	mgr.RegisterStrategy(v2, true)

	ctx := context.Background()

	// Decrypt v1 data
	decrypted, err := mgr.Decrypt(ctx, []byte("enc:v1:ciphertext"))
	require.NoError(t, err)
	assert.Equal(t, []byte("decrypted-by-v1"), decrypted)

	// Decrypt v2 data
	decrypted, err = mgr.Decrypt(ctx, []byte("enc:v2:ciphertext"))
	require.NoError(t, err)
	assert.Equal(t, []byte("decrypted-by-v2"), decrypted)
}

func TestManager_DecryptInvalidFormat(t *testing.T) {
	mgr := NewManager()
	ctx := context.Background()

	tests := []struct {
		name string
		data string
	}{
		{
			name: "When ciphertext part is missing it should error",
			data: "enc:v1",
		},
		{
			name: "When only prefix is present it should error",
			data: "enc:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mgr.Decrypt(ctx, []byte(tt.data))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "invalid encrypted format")
		})
	}
}

func TestManager_DecryptUnknownVersion(t *testing.T) {
	mgr := NewManager()
	v1 := &mockStrategy{version: "v1"}
	mgr.RegisterStrategy(v1, true)

	ctx := context.Background()

	_, err := mgr.Decrypt(ctx, []byte("enc:v99:ciphertext"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no decryption strategy registered for version")
}

// mockStrategy is a test helper that implements the Strategy interface
type mockStrategy struct {
	version           string
	activeKeyID       string
	encryptFunc       func(ctx context.Context, data []byte) ([]byte, error)
	parseBodyFunc     func(body []byte) (*ParsedEncrypted, error)
	decryptParsedFunc func(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error)
}

func (m *mockStrategy) Version() string {
	return m.version
}

func (m *mockStrategy) String() string {
	return "mock-algorithm, active_key=" + m.activeKeyID
}

func (m *mockStrategy) ActiveKeyID() string {
	return m.activeKeyID
}

func (m *mockStrategy) Algorithm() string {
	return "mock-algorithm"
}

func (m *mockStrategy) ConfiguredKeys() []string {
	return []string{m.activeKeyID}
}

func (m *mockStrategy) EncryptPlaintext(ctx context.Context, plaintext []byte) ([]byte, error) {
	if m.encryptFunc != nil {
		return m.encryptFunc(ctx, plaintext)
	}
	return []byte("encrypted"), nil
}

func (m *mockStrategy) ParseBody(body []byte) (*ParsedEncrypted, error) {
	if m.parseBodyFunc != nil {
		return m.parseBodyFunc(body)
	}
	return &ParsedEncrypted{KeyID: m.activeKeyID, Payload: body}, nil
}

func (m *mockStrategy) DecryptParsed(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error) {
	if m.decryptParsedFunc != nil {
		return m.decryptParsedFunc(ctx, parsed)
	}
	return []byte("decrypted"), nil
}

// TestProcessEncryption tests the smart re-encryption logic

func TestProcessEncryption_Plaintext(t *testing.T) {
	var err error
	mgr := NewManager()

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)

	v1 := newV1Strategy()
	require.NoError(t, v1.AddKey("default", key, true))
	mgr.RegisterStrategy(v1, true)

	ctx := context.Background()
	plaintext := []byte("my-secret")

	result, err := mgr.ProcessEncryption(ctx, plaintext)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(string(result), "enc:v1:default:"))
	assert.NotEqual(t, plaintext, result)

	// Should decrypt back
	decrypted, err := mgr.Decrypt(ctx, result)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestProcessEncryption_AlreadyEncrypted_SameKey_Preserves(t *testing.T) {
	var err error
	mgr := NewManager()

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)

	v1 := newV1Strategy()
	require.NoError(t, v1.AddKey("default", key, true))
	mgr.RegisterStrategy(v1, true)

	ctx := context.Background()
	plaintext := []byte("my-secret")

	// First encryption
	encrypted, err := mgr.Encrypt(ctx, plaintext)
	require.NoError(t, err)

	// ProcessEncryption should preserve (same version, same key)
	result, err := mgr.ProcessEncryption(ctx, encrypted)
	require.NoError(t, err)

	// CRITICAL: Should be EXACTLY the same (no wasteful re-encryption)
	assert.Equal(t, encrypted, result, "Already-encrypted data with active key should be preserved")
}

func TestProcessEncryption_KeyRotation_Reencrypts(t *testing.T) {
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

	// Set keyA as active
	_ = v1.SetActiveKey("keyA")
	mgr.RegisterStrategy(v1, true)

	ctx := context.Background()
	plaintext := []byte("my-secret")

	// Encrypt with keyA
	encrypted, err := mgr.Encrypt(ctx, plaintext)
	require.NoError(t, err)
	assert.Contains(t, string(encrypted), "keyA:", "Should be encrypted with keyA")

	// Switch to keyB
	_ = v1.SetActiveKey("keyB")

	// ProcessEncryption should re-encrypt with keyB
	result, err := mgr.ProcessEncryption(ctx, encrypted)
	require.NoError(t, err)

	// Should be DIFFERENT (new key = new encryption)
	assert.NotEqual(t, encrypted, result, "Re-encryption should produce different ciphertext")
	assert.Contains(t, string(result), "keyB:", "Should now be encrypted with keyB")

	// But should still decrypt to same plaintext
	decrypted, err := mgr.Decrypt(ctx, result)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestProcessEncryption_VersionMigration(t *testing.T) {
	var err error

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)

	// Create v1 manager
	mgrV1 := NewManager()
	v1 := newV1Strategy()
	require.NoError(t, v1.AddKey("default", key, true))
	mgrV1.RegisterStrategy(v1, true)

	ctx := context.Background()
	plaintext := []byte("my-secret")

	// Encrypt with v1
	encryptedV1, err := mgrV1.Encrypt(ctx, plaintext)
	require.NoError(t, err)
	assert.Contains(t, string(encryptedV1), "enc:v1:")

	// Create v2 manager with both strategies
	mgrV2 := NewManager()
	mgrV2.RegisterStrategy(v1, false) // Keep v1 for decryption (not active)

	v2 := &mockStrategy{
		version:     "v2",
		activeKeyID: "default",
		encryptFunc: func(ctx context.Context, data []byte) ([]byte, error) {
			return []byte("default:v2-encrypted-data"), nil
		},
		parseBodyFunc: func(body []byte) (*ParsedEncrypted, error) {
			return &ParsedEncrypted{KeyID: "default", Payload: body}, nil
		},
		decryptParsedFunc: func(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error) {
			return plaintext, nil
		},
	}
	mgrV2.RegisterStrategy(v2, true) // v2 becomes active

	// ProcessEncryption should migrate from v1 to v2
	result, err := mgrV2.ProcessEncryption(ctx, encryptedV1)
	require.NoError(t, err)

	// Should now be v2
	assert.Contains(t, string(result), "enc:v2:", "Should be migrated to v2")
	assert.NotEqual(t, encryptedV1, result, "Migration should produce different format")
}

func TestProcessEncryption_Idempotent_MultipleRoundTrips(t *testing.T) {
	var err error
	mgr := NewManager()

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)

	v1 := newV1Strategy()
	require.NoError(t, v1.AddKey("default", key, true))
	mgr.RegisterStrategy(v1, true)

	ctx := context.Background()
	plaintext := []byte("my-secret")

	// First process
	result1, err := mgr.ProcessEncryption(ctx, plaintext)
	require.NoError(t, err)

	// Second process (should preserve)
	result2, err := mgr.ProcessEncryption(ctx, result1)
	require.NoError(t, err)

	// Third process (should still preserve)
	result3, err := mgr.ProcessEncryption(ctx, result2)
	require.NoError(t, err)

	// All results after first should be identical (idempotent)
	assert.Equal(t, result1, result2, "Second call should preserve")
	assert.Equal(t, result2, result3, "Third call should preserve")
}
