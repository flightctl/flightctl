package encryption

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

const canaryInitTimeout = 30 * time.Second

var (
	globalManager     *Manager
	globalManagerOnce sync.Once
	globalManagerMu   sync.RWMutex
	globalInitErr     error // Cached initialization error from sync.Once attempt
)

// Plaintext is a type-safe wrapper for plaintext data.
type Plaintext []byte

// Ciphertext is a type-safe wrapper for encrypted data.
type Ciphertext []byte

// String returns the ciphertext as a string (typically starts with "enc:").
func (c Ciphertext) String() string {
	return string(c)
}

// Bytes returns the ciphertext as bytes.
func (c Ciphertext) Bytes() []byte {
	return []byte(c)
}

// String returns the plaintext as a string.
func (p Plaintext) String() string {
	return string(p)
}

// Bytes returns the plaintext as bytes.
func (p Plaintext) Bytes() []byte {
	return []byte(p)
}

// InitGlobalEncryption initializes the global encryption manager from the
// application config (cfg.Encryption). Must be called once at startup before
// any concurrent access. Uses sync.Once internally.
func InitGlobalEncryption(log logrus.FieldLogger, cfg *config.Config) error {
	return InitGlobalEncryptionFull(log, cfg, nil, nil)
}

// InitGlobalEncryptionWithCanary initializes encryption from the application
// config with an optional canary store for key-health verification.
func InitGlobalEncryptionWithCanary(log logrus.FieldLogger, cfg *config.Config, canaryStore CanaryStore) error {
	return InitGlobalEncryptionFull(log, cfg, canaryStore, nil)
}

// InitGlobalEncryptionFull initializes encryption from the application config
// with optional canary store and metrics recorder.
func InitGlobalEncryptionFull(log logrus.FieldLogger, cfg *config.Config, canaryStore CanaryStore, metrics MetricsRecorder) error {
	globalManagerOnce.Do(func() {
		v1Strategy, err := NewV1Strategy(cfg)
		if err != nil {
			globalInitErr = fmt.Errorf("load encryption key: %w", err)
			return
		}

		manager := NewManager()
		manager.RegisterStrategy(v1Strategy, true)

		// Set metrics recorder if provided
		if metrics != nil {
			manager.SetMetricsRecorder(metrics)
		}

		// Set canary store if provided
		if canaryStore != nil {
			manager.SetCanaryStore(canaryStore)
		}

		globalManagerMu.Lock()
		globalManager = manager
		globalManagerMu.Unlock()

		activeVersion, activeStrategy := manager.GetActiveStrategy()
		var activeKeyID string
		if activeStrategy != nil {
			activeKeyID = activeStrategy.ActiveKeyID()
		}
		log.Infof("Encryption initialized: active=%s/%s", activeVersion, activeKeyID)
	})

	return globalInitErr
}

// GlobalManager returns the global encryption manager.
// Returns nil if InitGlobalEncryption has not been called.
//
// Thread safety: Safe for concurrent access after InitGlobalEncryption completes.
// The returned Manager is read-only (no RegisterStrategy/SetActiveStrategy calls after init).
func GlobalManager() *Manager {
	globalManagerMu.RLock()
	defer globalManagerMu.RUnlock()

	return globalManager // Can be nil if not initialized
}

// InitCanaryStore sets the canary store on the global manager, creates a canary
// for the active key, and validates all stored canaries. No-op if encryption is
// not initialized. Applies a fixed 30 s timeout so callers can't drift.
func InitCanaryStore(ctx context.Context, store CanaryStore) error {
	mgr := GlobalManager()
	if mgr == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, canaryInitTimeout)
	defer cancel()

	mgr.SetCanaryStore(store)
	if err := mgr.EnsureActiveCanary(ctx); err != nil {
		return fmt.Errorf("ensuring encryption canary: %w", err)
	}
	if err := mgr.ValidateCanaries(ctx); err != nil {
		return fmt.Errorf("validating encryption canaries: %w", err)
	}
	return nil
}

// Encrypt is a type-safe convenience function that encrypts using the global manager.
// Takes Plaintext, returns Ciphertext - type system prevents swapping arguments.
//
// Thread safety: Safe for concurrent use after InitGlobalEncryption completes.
func Encrypt(ctx context.Context, plaintext Plaintext) (Ciphertext, error) {
	mgr := GlobalManager()
	if mgr == nil {
		return nil, fmt.Errorf("encryption not initialized - call InitGlobalEncryption first")
	}

	encrypted, err := mgr.Encrypt(ctx, plaintext.Bytes())
	if err != nil {
		return nil, err
	}
	return Ciphertext(encrypted), nil
}

// Decrypt decrypts ciphertext using the global manager.
// Returns (plaintext, ok, error) where ok indicates if decryption was performed.
// - If input has "enc:" prefix: decrypts and returns (plaintext, true, nil)
// - If input has no "enc:" prefix (backward compatibility): returns (input, false, nil)
// - On error: returns (nil, false, error)
//
// Thread safety: Safe for concurrent use after InitGlobalEncryption completes.
func Decrypt(ctx context.Context, ciphertext Ciphertext) (Plaintext, bool, error) {
	mgr := GlobalManager()
	if mgr == nil {
		return nil, false, fmt.Errorf("encryption not initialized - call InitGlobalEncryption first")
	}

	wasEncrypted := IsEncrypted(ciphertext.Bytes())

	decrypted, err := mgr.Decrypt(ctx, ciphertext.Bytes())
	if err != nil {
		return nil, false, err
	}
	return Plaintext(decrypted), wasEncrypted, nil
}
