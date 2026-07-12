package encryption

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/pkg/crypto"
)

// defaultKeyFile returns the default path for the v1 encryption key file.
// This is $HOME/.flightctl/encryption/key (e.g., /root/.flightctl/encryption/key).
func defaultKeyFile() string {
	return filepath.Join(config.ConfigDir(), "encryption", "key")
}

// V1Strategy implements AES-256-GCM encryption.
// Format: keyID:base64(nonce||ciphertext||tag)
// The keyID prefix is always present, enabling key rotation detection.
//
// Thread safety: V1Strategy is safe for concurrent use. All public methods are protected
type V1Strategy struct {
	mu        sync.RWMutex
	keys      map[string][]byte      // keyID -> key (32 bytes each)
	activeKey string                 // Which key to use for new encryptions
	gcms      map[string]cipher.AEAD // Cached GCM instances per key
}

func newV1Strategy() *V1Strategy {
	return &V1Strategy{
		keys: make(map[string][]byte),
		gcms: make(map[string]cipher.AEAD),
	}
}

// NewV1Strategy loads a single encryption key from environment variable or file.
// Priority: FLIGHTCTL_ENCRYPTION_KEY env var > defaultKeyFile()
// The key is registered with keyID "default".
func NewV1Strategy() (*V1Strategy, error) {
	strategy := newV1Strategy()

	var encodedKey string
	keyPath := defaultKeyFile()

	// Try environment variable first
	if envKey := os.Getenv("FLIGHTCTL_ENCRYPTION_KEY"); envKey != "" {
		encodedKey = strings.TrimSpace(envKey)
	} else {
		// Try default key file
		keyBytes, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read key file %s: %w", keyPath, err)
		}
		encodedKey = strings.TrimSpace(string(keyBytes))
	}

	key, err := crypto.DecodeAES256Key(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("decode encryption key: %w", err)
	}

	if err := strategy.AddKey("default", key, true); err != nil {
		return nil, err
	}

	return strategy, nil
}

// AddKey registers an encryption key with the given ID.
// If setActive is true, this key becomes the active key for new encryptions.
// key must be 32 bytes (AES-256).
func (s *V1Strategy) AddKey(keyID string, key []byte, setActive bool) error {
	if len(key) != 32 {
		return fmt.Errorf("v1 strategy requires 32-byte key, got %d bytes", len(key))
	}

	// Check for zeroed-out key (configuration error)
	zeroKey := make([]byte, 32)
	if bytes.Equal(key, zeroKey) {
		return fmt.Errorf("key %s is all zeros - likely a configuration error", keyID)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("create AES cipher for key %s: %w", keyID, err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create GCM for key %s: %w", keyID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.keys[keyID] = key
	s.gcms[keyID] = gcm
	if setActive {
		s.activeKey = keyID
	}
	return nil
}

// SetActiveKey sets which key will be used for new encryptions.
func (s *V1Strategy) SetActiveKey(keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.keys[keyID]; !exists {
		return fmt.Errorf("%w: key %s in v1 strategy", ErrKeyNotFound, keyID)
	}
	s.activeKey = keyID
	return nil
}

// Version returns "v1" (immutable version identifier).
func (s *V1Strategy) Version() string {
	return "v1"
}

// Algorithm returns the algorithm name.
func (s *V1Strategy) Algorithm() string {
	return "AES-256-GCM"
}

// ConfiguredKeys returns all configured key IDs.
func (s *V1Strategy) ConfiguredKeys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keyIDs := make([]string, 0, len(s.keys))
	for keyID := range s.keys {
		keyIDs = append(keyIDs, keyID)
	}
	return keyIDs
}

// String returns human-readable status information about this strategy.
func (s *V1Strategy) String() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keyIDs := make([]string, 0, len(s.keys))
	for keyID := range s.keys {
		keyIDs = append(keyIDs, keyID)
	}
	return fmt.Sprintf("%s, active_key=%s, keys=%v", s.Algorithm(), s.activeKey, keyIDs)
}

// ActiveKeyID returns the identifier of the currently active encryption key.
func (s *V1Strategy) ActiveKeyID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeKey
}

// EncryptPlaintext encrypts plaintext using AES-256-GCM with the active key.
// Returns format: keyID:base64(nonce||ciphertext||tag)
func (s *V1Strategy) EncryptPlaintext(ctx context.Context, plaintext []byte) ([]byte, error) {
	s.mu.RLock()
	activeKey := s.activeKey
	gcm, exists := s.gcms[activeKey]
	s.mu.RUnlock()

	if activeKey == "" {
		return nil, fmt.Errorf("no active key set in v1 strategy")
	}
	if !exists {
		return nil, fmt.Errorf("active key %s not found", activeKey)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Encrypt: returns nonce || ciphertext || tag
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	// Base64 encode the result
	encoded := base64.StdEncoding.EncodeToString(ciphertext)

	// Return keyID:base64data
	return []byte(fmt.Sprintf("%s:%s", activeKey, encoded)), nil
}

// ParseBody parses v1 format: keyID:base64(nonce||ciphertext||tag)
func (s *V1Strategy) ParseBody(body []byte) (*ParsedEncrypted, error) {
	str := string(body)

	// Parse keyID:payload format
	parts := strings.SplitN(str, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid v1 format: expected keyID:base64data, got %q", str)
	}

	keyID := parts[0]
	encodedData := parts[1]

	// Base64 decode
	decoded, err := base64.StdEncoding.DecodeString(encodedData)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	return &ParsedEncrypted{
		KeyID:   keyID,
		Payload: decoded,
	}, nil
}

// DecryptParsed decrypts a parsed v1 encrypted value using AES-256-GCM.
func (s *V1Strategy) DecryptParsed(ctx context.Context, parsed *ParsedEncrypted) ([]byte, error) {
	s.mu.RLock()
	gcm, exists := s.gcms[parsed.KeyID]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("key %s not found for decryption", parsed.KeyID)
	}

	nonceSize := gcm.NonceSize()
	if len(parsed.Payload) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short: expected at least %d bytes, got %d", nonceSize, len(parsed.Payload))
	}

	// Split nonce and ciphertext
	nonce := parsed.Payload[:nonceSize]
	ciphertextData := parsed.Payload[nonceSize:]

	// Decrypt
	plaintext, err := gcm.Open(nil, nonce, ciphertextData, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt with key %s: %w", parsed.KeyID, err)
	}

	return plaintext, nil
}
