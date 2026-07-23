package encryption

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspectEncrypted(t *testing.T) {
	mgr := NewManager()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	v1 := newV1Strategy()
	require.NoError(t, v1.AddKey("default", key, true))
	mgr.RegisterStrategy(v1, true)

	ctx := context.Background()

	t.Run("When plaintext it should report not encrypted", func(t *testing.T) {
		version, keyID, encrypted, err := InspectEncrypted([]byte("plain-secret"), mgr)
		require.NoError(t, err)
		assert.False(t, encrypted)
		assert.Empty(t, version)
		assert.Empty(t, keyID)
	})

	t.Run("When encrypted with v1 it should return version and key ID", func(t *testing.T) {
		ciphertext, err := mgr.Encrypt(ctx, []byte("plain-secret"))
		require.NoError(t, err)

		version, keyID, encrypted, err := InspectEncrypted(ciphertext, mgr)
		require.NoError(t, err)
		assert.True(t, encrypted)
		assert.Equal(t, "v1", version)
		assert.Equal(t, "default", keyID)
	})

	t.Run("When enc prefix is malformed it should return an error", func(t *testing.T) {
		_, _, encrypted, err := InspectEncrypted([]byte("enc:v1"), mgr)
		require.Error(t, err)
		assert.False(t, encrypted)
	})

	t.Run("When strategy body is malformed it should return an error", func(t *testing.T) {
		_, _, encrypted, err := InspectEncrypted([]byte("enc:v1:not-a-valid-body"), mgr)
		require.Error(t, err)
		assert.False(t, encrypted)
	})

	t.Run("When strategy version is unknown it should return an error", func(t *testing.T) {
		_, _, encrypted, err := InspectEncrypted([]byte("enc:v99:default:abc"), mgr)
		require.Error(t, err)
		assert.False(t, encrypted)
	})

	t.Run("When manager is nil it should return an error for encrypted data", func(t *testing.T) {
		ciphertext, err := mgr.Encrypt(ctx, []byte("plain-secret"))
		require.NoError(t, err)

		_, _, encrypted, err := InspectEncrypted(ciphertext, nil)
		require.Error(t, err)
		assert.False(t, encrypted)
	})
}
