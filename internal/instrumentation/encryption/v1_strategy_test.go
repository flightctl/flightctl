package encryption

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewV1Strategy_EmptyConstructor(t *testing.T) {
	strategy := newV1Strategy()

	assert.NotNil(t, strategy)
	assert.Empty(t, strategy.keys)
	assert.Empty(t, strategy.gcms)
	assert.Empty(t, strategy.ActiveKeyID())
}

func TestV1Strategy_AddKey(t *testing.T) {
	strategy := newV1Strategy()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)

	require.NoError(t, strategy.AddKey("my-key", key, true))

	assert.Contains(t, strategy.keys, "my-key")
	assert.Contains(t, strategy.gcms, "my-key")
	assert.Equal(t, "my-key", strategy.ActiveKeyID(), "newly added key should be active")
}

func TestV1Strategy_AddKey_InvalidKeySize(t *testing.T) {
	strategy := newV1Strategy()

	tests := []struct {
		name    string
		keySize int
	}{
		{"When key is too short it should error", 16},
		{"When key is too long it should error", 64},
		{"When key is empty it should error", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := make([]byte, tt.keySize)
			err := strategy.AddKey("test-key", key, true)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "32-byte key")
		})
	}
}

func TestV1Strategy_AddMultipleKeys(t *testing.T) {
	strategy := newV1Strategy()

	encodedKey1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key1, err := base64.StdEncoding.DecodeString(encodedKey1)
	require.NoError(t, err)

	encodedKey2, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key2, err := base64.StdEncoding.DecodeString(encodedKey2)
	require.NoError(t, err)

	err = strategy.AddKey("2024-01", key1, true)
	require.NoError(t, err)
	assert.Equal(t, "2024-01", strategy.ActiveKeyID())

	err = strategy.AddKey("2024-06", key2, true)
	require.NoError(t, err)
	assert.Equal(t, "2024-06", strategy.ActiveKeyID(), "last added key should be active")

	assert.Len(t, strategy.keys, 2, "both keys should be registered")
}

func TestV1Strategy_SetActiveKey(t *testing.T) {
	strategy := newV1Strategy()

	encodedKey1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key1, err := base64.StdEncoding.DecodeString(encodedKey1)
	require.NoError(t, err)

	encodedKey2, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key2, err := base64.StdEncoding.DecodeString(encodedKey2)
	require.NoError(t, err)

	require.NoError(t, strategy.AddKey("2024-01", key1, true))
	require.NoError(t, strategy.AddKey("2024-06", key2, true))

	// Switch back to old key
	err = strategy.SetActiveKey("2024-01")
	require.NoError(t, err)
	assert.Equal(t, "2024-01", strategy.ActiveKeyID())

	// Try to set non-existent key
	err = strategy.SetActiveKey("2024-99")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyNotFound)
}

func TestV1Strategy_Version(t *testing.T) {
	strategy := newV1Strategy()
	assert.Equal(t, "v1", strategy.Version())
}

func TestV1Strategy_EncryptDecrypt_SingleKey(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)

	err = strategy.AddKey("default", key, true)
	require.NoError(t, err)

	plaintext := []byte("my-secret-password-123")

	// Encrypt
	encrypted, err := strategy.EncryptPlaintext(ctx, plaintext)
	require.NoError(t, err)
	assert.NotEmpty(t, encrypted)
	assert.NotEqual(t, plaintext, encrypted)

	// Should have keyID:base64 format
	encryptedStr := string(encrypted)
	assert.Contains(t, encryptedStr, "default:", "should have keyID prefix")
	parts := strings.SplitN(encryptedStr, ":", 2)
	require.Len(t, parts, 2, "should have keyID:payload format")
	_, err = base64.StdEncoding.DecodeString(parts[1])
	assert.NoError(t, err, "payload after keyID should be valid base64")

	// Decrypt
	decrypted, err := testDecrypt(t, strategy, ctx, encrypted)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestV1Strategy_EncryptDecrypt_MultipleKeys(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key1, err := base64.StdEncoding.DecodeString(encodedKey1)
	require.NoError(t, err)

	encodedKey2, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key2, err := base64.StdEncoding.DecodeString(encodedKey2)
	require.NoError(t, err)

	err = strategy.AddKey("2024-01", key1, true)
	require.NoError(t, err)

	// Encrypt with first key
	plaintext1 := []byte("encrypted-with-key1")
	encrypted1, err := strategy.EncryptPlaintext(ctx, plaintext1)
	require.NoError(t, err)

	// Should have keyID prefix since we have multiple keys
	assert.True(t, strings.HasPrefix(string(encrypted1), "2024-01:"), "should include keyID prefix")

	// Add second key (becomes active)
	err = strategy.AddKey("2024-06", key2, true)
	require.NoError(t, err)

	// Encrypt with second key
	plaintext2 := []byte("encrypted-with-key2")
	encrypted2, err := strategy.EncryptPlaintext(ctx, plaintext2)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(encrypted2), "2024-06:"), "should include new keyID prefix")

	// Can decrypt both
	decrypted1, err := testDecrypt(t, strategy, ctx, encrypted1)
	require.NoError(t, err)
	assert.Equal(t, plaintext1, decrypted1)

	decrypted2, err := testDecrypt(t, strategy, ctx, encrypted2)
	require.NoError(t, err)
	assert.Equal(t, plaintext2, decrypted2)
}

func TestV1Strategy_Decrypt_InvalidBase64(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("default", key, true))

	_, err = testDecrypt(t, strategy, ctx, []byte("default:not-valid-base64!!!"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "base64 decode")
}

func TestV1Strategy_Decrypt_TooShort(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("default", key, true))

	// Valid base64 but too short to contain nonce
	shortData := base64.StdEncoding.EncodeToString([]byte("x"))
	_, err = testDecrypt(t, strategy, ctx, []byte(fmt.Sprintf("default:%s", shortData)))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestV1Strategy_Decrypt_InvalidCiphertext(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("default", key, true))

	// Valid base64, right size, but invalid ciphertext (will fail GCM authentication)
	fakeData := make([]byte, 32) // Enough bytes to pass length check
	_, err = rand.Read(fakeData)
	require.NoError(t, err)
	encoded := base64.StdEncoding.EncodeToString(fakeData)

	_, err = testDecrypt(t, strategy, ctx, []byte(fmt.Sprintf("default:%s", encoded)))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt")
}

func TestV1Strategy_Decrypt_UnknownKeyID(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("2024-01", key, true))
	require.NoError(t, strategy.AddKey("2024-06", key, true))

	// Encrypt with one of the known keys to create a real encrypted blob
	plaintext := []byte("test-data")
	encrypted, err := strategy.EncryptPlaintext(ctx, plaintext)
	require.NoError(t, err)

	// Now replace the keyID with an unknown one
	parts := strings.Split(string(encrypted), ":")
	require.Len(t, parts, 2, "should have keyID:ciphertext format")

	fakeData := "unknown-key-2099:" + parts[1]

	// This should fail because unknown-key-2099 doesn't exist in the map
	_, err = testDecrypt(t, strategy, ctx, []byte(fakeData))
	assert.Error(t, err)
	// The error might be "key unknown-key-2099 not found" OR "base64 decode"
	// depending on fallback logic - either is acceptable
	assert.True(t,
		strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "base64"),
		"should error on unknown key, got: %v", err)
}

func TestV1Strategy_EncryptDecrypt_LargePayload(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("default", key, true))

	// 1MB payload
	largePlaintext := make([]byte, 1024*1024)
	_, err = rand.Read(largePlaintext)
	require.NoError(t, err)

	encrypted, err := strategy.EncryptPlaintext(ctx, largePlaintext)
	require.NoError(t, err)

	decrypted, err := testDecrypt(t, strategy, ctx, encrypted)
	require.NoError(t, err)
	assert.Equal(t, largePlaintext, decrypted)
}

func TestV1Strategy_EncryptDecrypt_EmptyData(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("default", key, true))

	plaintext := []byte("")

	encrypted, err := strategy.EncryptPlaintext(ctx, plaintext)
	require.NoError(t, err)

	decrypted, err := testDecrypt(t, strategy, ctx, encrypted)
	require.NoError(t, err)
	// GCM returns nil for empty plaintext, which is semantically equivalent to []byte{}
	assert.Empty(t, decrypted)
}

func TestV1Strategy_UniqueNonces(t *testing.T) {
	strategy := newV1Strategy()
	ctx := context.Background()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("default", key, true))

	plaintext := []byte("same-plaintext")

	// Encrypt the same plaintext multiple times
	encrypted1, err := strategy.EncryptPlaintext(ctx, plaintext)
	require.NoError(t, err)

	encrypted2, err := strategy.EncryptPlaintext(ctx, plaintext)
	require.NoError(t, err)

	// Ciphertexts should be different (due to unique nonces)
	assert.NotEqual(t, encrypted1, encrypted2, "same plaintext should produce different ciphertexts")

	// But both should decrypt to the same plaintext
	decrypted1, _ := testDecrypt(t, strategy, ctx, encrypted1)
	decrypted2, _ := testDecrypt(t, strategy, ctx, encrypted2)
	assert.Equal(t, plaintext, decrypted1)
	assert.Equal(t, plaintext, decrypted2)
}

func TestNewV1Strategy_EnvVar(t *testing.T) {
	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)

	t.Setenv("FLIGHTCTL_ENCRYPTION_KEY", encodedKey)

	strategy, err := NewV1Strategy()
	require.NoError(t, err)
	assert.NotNil(t, strategy)
	assert.Equal(t, "default", strategy.ActiveKeyID())
	assert.Contains(t, strategy.keys, "default")
}

func TestNewV1Strategy_NoKeyProvided(t *testing.T) {
	os.Unsetenv("FLIGHTCTL_ENCRYPTION_KEY")

	_, err := NewV1Strategy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read key file")
}

func TestNewV1Strategy_InvalidBase64(t *testing.T) {
	t.Setenv("FLIGHTCTL_ENCRYPTION_KEY", "not-valid-base64!!!")

	_, err := NewV1Strategy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestNewV1Strategy_KeyTooShort(t *testing.T) {
	var err error

	shortKey := make([]byte, 16) // Only 16 bytes, need 32
	_, err = rand.Read(shortKey)
	require.NoError(t, err)
	encodedKey := base64.StdEncoding.EncodeToString(shortKey)

	t.Setenv("FLIGHTCTL_ENCRYPTION_KEY", encodedKey)

	_, err = NewV1Strategy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "32 bytes")
}

func TestGenerateAES256Key(t *testing.T) {
	key1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	assert.NotEmpty(t, key1)

	key2, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	assert.NotEmpty(t, key2)

	// Keys should be different
	assert.NotEqual(t, key1, key2, "generated keys should be unique")

	// Should be valid base64
	decoded, err := base64.StdEncoding.DecodeString(key1)
	require.NoError(t, err)
	assert.Len(t, decoded, 32, "decoded key should be 32 bytes")
}

func TestV1Strategy_String(t *testing.T) {
	strategy := newV1Strategy()

	encodedKey1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key1, err := base64.StdEncoding.DecodeString(encodedKey1)
	require.NoError(t, err)
	err = strategy.AddKey("default", key1, true)
	require.NoError(t, err)

	str := strategy.String()
	assert.Contains(t, str, "AES-256-GCM", "should include algorithm name")
	assert.Contains(t, str, "active_key=default", "should include active key")
	assert.Contains(t, str, "keys=", "should list configured keys")
}

func TestV1Strategy_ActiveKeyID(t *testing.T) {
	strategy := newV1Strategy()

	encodedKey1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key1, err := base64.StdEncoding.DecodeString(encodedKey1)
	require.NoError(t, err)
	err = strategy.AddKey("default", key1, true)
	require.NoError(t, err)

	assert.Equal(t, "default", strategy.ActiveKeyID())

	// Add another key
	encodedKey2, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key2, err := base64.StdEncoding.DecodeString(encodedKey2)
	require.NoError(t, err)
	err = strategy.AddKey("key2", key2, true)
	require.NoError(t, err)

	// Should update to new key (AddKey makes it active)
	assert.Equal(t, "key2", strategy.ActiveKeyID())

	// Set back to default
	err = strategy.SetActiveKey("default")
	require.NoError(t, err)

	assert.Equal(t, "default", strategy.ActiveKeyID())
}

func TestV1Strategy_String_MultipleKeys(t *testing.T) {
	strategy := newV1Strategy()

	encodedKey1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key1, err := base64.StdEncoding.DecodeString(encodedKey1)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("default", key1, true))

	encodedKey2, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key2, err := base64.StdEncoding.DecodeString(encodedKey2)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("keyB", key2, true))

	str := strategy.String()
	assert.Contains(t, str, "AES-256-GCM")
	assert.Contains(t, str, "active_key=keyB", "latest added key should be active")
	assert.Contains(t, str, "default", "should list first key")
	assert.Contains(t, str, "keyB", "should list second key")
}

func TestV1Strategy_Algorithm(t *testing.T) {
	strategy := newV1Strategy()

	algo := strategy.Algorithm()
	assert.Equal(t, "AES-256-GCM", algo)
}

func TestV1Strategy_ConfiguredKeys_Single(t *testing.T) {
	strategy := newV1Strategy()

	encodedKey, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("default", key, true))

	keys := strategy.ConfiguredKeys()
	assert.Len(t, keys, 1)
	assert.Contains(t, keys, "default")
}

func TestV1Strategy_ConfiguredKeys_Multiple(t *testing.T) {
	strategy := newV1Strategy()

	encodedKey1, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key1, err := base64.StdEncoding.DecodeString(encodedKey1)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("keyA", key1, true))

	encodedKey2, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key2, err := base64.StdEncoding.DecodeString(encodedKey2)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("keyB", key2, true))

	encodedKey3, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	key3, err := base64.StdEncoding.DecodeString(encodedKey3)
	require.NoError(t, err)
	require.NoError(t, strategy.AddKey("keyC", key3, true))

	keys := strategy.ConfiguredKeys()
	assert.Len(t, keys, 3)
	assert.Contains(t, keys, "keyA")
	assert.Contains(t, keys, "keyB")
	assert.Contains(t, keys, "keyC")
}

// testDecrypt is a helper that uses the new ParseBody + DecryptParsed interface
func testDecrypt(t *testing.T, strategy *V1Strategy, ctx context.Context, encrypted []byte) ([]byte, error) {
	t.Helper()
	parsed, err := strategy.ParseBody(encrypted)
	if err != nil {
		return nil, err
	}
	return strategy.DecryptParsed(ctx, parsed)
}

func TestV1Strategy_AddKey_RejectsZeroKey(t *testing.T) {
	strategy := newV1Strategy()

	zeroKey := make([]byte, 32) // All zeros

	err := strategy.AddKey("zero-key", zeroKey, true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "all zeros")
	assert.Contains(t, err.Error(), "configuration error")
}
