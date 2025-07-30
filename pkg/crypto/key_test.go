package crypto

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicKeysEqual(t *testing.T) {
	require := require.New(t)

	// Generate test key pairs
	rsaKey1, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(err)
	rsaKey2, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(err)

	ecdsaKey1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(err)
	ecdsaKey2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(err)

	tests := []struct {
		name     string
		key1     crypto.PublicKey
		key2     crypto.PublicKey
		expected bool
	}{
		{
			name:     "same RSA key",
			key1:     &rsaKey1.PublicKey,
			key2:     &rsaKey1.PublicKey,
			expected: true,
		},
		{
			name:     "different RSA keys",
			key1:     &rsaKey1.PublicKey,
			key2:     &rsaKey2.PublicKey,
			expected: false,
		},
		{
			name:     "same ECDSA key",
			key1:     &ecdsaKey1.PublicKey,
			key2:     &ecdsaKey1.PublicKey,
			expected: true,
		},
		{
			name:     "different ECDSA keys",
			key1:     &ecdsaKey1.PublicKey,
			key2:     &ecdsaKey2.PublicKey,
			expected: false,
		},
		{
			name:     "RSA vs ECDSA",
			key1:     &rsaKey1.PublicKey,
			key2:     &ecdsaKey1.PublicKey,
			expected: false,
		},
		{
			name:     "RSA key pointer vs pointer",
			key1:     &rsaKey1.PublicKey,
			key2:     &rsaKey1.PublicKey,
			expected: true,
		},
		{
			name:     "ECDSA key pointer vs pointer",
			key1:     &ecdsaKey1.PublicKey,
			key2:     &ecdsaKey1.PublicKey,
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := PublicKeysEqual(tc.key1, tc.key2)
			require.Equal(tc.expected, result)
		})
	}
}

func TestPublicKeysEqual_ErrorHandling(t *testing.T) {
	require := require.New(t)

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(err)

	tests := []struct {
		name     string
		key1     crypto.PublicKey
		key2     crypto.PublicKey
		expected bool
	}{
		{
			name:     "nil key1",
			key1:     nil,
			key2:     &rsaKey.PublicKey,
			expected: false,
		},
		{
			name:     "nil key2",
			key1:     &rsaKey.PublicKey,
			key2:     nil,
			expected: false,
		},
		{
			name:     "both nil",
			key1:     nil,
			key2:     nil,
			expected: false,
		},
		{
			name:     "unsupported key type",
			key1:     "not-a-key",
			key2:     &rsaKey.PublicKey,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := PublicKeysEqual(tc.key1, tc.key2)
			require.Equal(tc.expected, result)
		})
	}
}
