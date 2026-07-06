package encryption

import (
	"context"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"
)

var (
	globalManager       *Manager
	globalCanaryManager *CanaryManager
	globalLogger        logrus.FieldLogger
	globalManagerOnce   sync.Once
	globalManagerMu     sync.RWMutex
	globalInitErr       error // Cached initialization error from sync.Once attempt
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

// InitGlobalEncryption initializes the global encryption manager.
// This MUST be called once during application startup, before any concurrent access.
// It loads the v1 encryption strategy from FLIGHTCTL_ENCRYPTION_KEY environment variable or the provided keyPath.
//
// Thread safety: This function uses sync.Once to ensure single initialization.
// After initialization completes, concurrent Encrypt/Decrypt calls are safe.
func InitGlobalEncryption(log logrus.FieldLogger) error {
	return InitGlobalEncryptionWithCanary(log, nil)
}

// InitGlobalEncryptionWithCanary initializes encryption with optional canary store.
// If canaryStore is nil, canary functionality is disabled.
func InitGlobalEncryptionWithCanary(log logrus.FieldLogger, canaryStore CanaryStore) error {
	globalManagerOnce.Do(func() {
		v1Strategy, err := NewV1Strategy()
		if err != nil {
			globalInitErr = fmt.Errorf("load encryption key: %w", err)
			return
		}

		manager := NewManager()
		manager.RegisterStrategy(v1Strategy, true)

		// Determine active strategy info for logging
		activeStrategy := manager.activeStrategy
		var activeKeyID string
		if strategy, exists := manager.strategies[activeStrategy]; exists {
			activeKeyID = strategy.ActiveKeyID()
		}
		strategyInfo := fmt.Sprintf("active=%s/%s", activeStrategy, activeKeyID)

		// Initialize canary manager (optional)
		var canaryManager *CanaryManager
		if canaryStore != nil {
			canaryManager = NewCanaryManager(manager, canaryStore)

			// Validate all existing canaries
			ctx := context.Background()
			results, err := canaryManager.ValidateAll(ctx)
			if err != nil {
				globalInitErr = fmt.Errorf("validate canaries: %w", err)
				return
			}

			// Check validation results
			okCount := 0
			failedCount := 0
			for _, result := range results {
				if result.Status != "ok" {
					isActive := (result.Strategy == activeStrategy && result.KeyID == activeKeyID)
					if isActive {
						// CRITICAL: Active key cannot decrypt its canary
						log.Errorf("CRITICAL: Active encryption key %s/%s cannot decrypt canary: %s",
							result.Strategy, result.KeyID, result.Error)
						globalInitErr = fmt.Errorf("active encryption key %s/%s is broken - cannot decrypt canary",
							result.Strategy, result.KeyID)
						return
					} else {
						// WARNING: Old key cannot decrypt
						log.Warnf("WARNING: Encryption key %s/%s cannot decrypt canary: %s - reads of data encrypted with this key will fail",
							result.Strategy, result.KeyID, result.Error)
						failedCount++
					}
				} else {
					okCount++
				}
			}

			// Log canary validation summary
			if len(results) > 0 {
				log.Infof("Encryption initialized: %s, validated %d canaries (%d ok, %d failed)",
					strategyInfo, len(results), okCount, failedCount)
			} else {
				log.Infof("Encryption initialized: %s, no existing canaries", strategyInfo)
			}
		} else {
			// No canary store - canaries disabled
			log.Infof("Encryption initialized: %s (canaries disabled)", strategyInfo)
		}

		globalManagerMu.Lock()
		globalManager = manager
		globalCanaryManager = canaryManager // Can be nil
		globalLogger = log
		globalManagerMu.Unlock()
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

// GlobalCanaryManager returns the global canary manager.
// Returns nil if canaries are disabled or InitGlobalEncryption has not been called.
//
// Thread safety: Safe for concurrent access after InitGlobalEncryption completes.
func GlobalCanaryManager() *CanaryManager {
	globalManagerMu.RLock()
	defer globalManagerMu.RUnlock()

	return globalCanaryManager // Can be nil if canaries disabled or not initialized
}

// Encrypt is a type-safe convenience function that encrypts using the global manager.
// Takes Plaintext, returns Ciphertext - type system prevents swapping arguments.
//
// On first call, automatically creates a canary for the active encryption key (if canaries enabled).
// This verifies encryption is working correctly.
//
// Thread safety: Safe for concurrent use after InitGlobalEncryption completes.
func Encrypt(ctx context.Context, plaintext Plaintext) (Ciphertext, error) {
	mgr := GlobalManager()
	if mgr == nil {
		return nil, fmt.Errorf("encryption not initialized - call InitGlobalEncryption first")
	}

	// Ensure canary exists for active key (if canary manager is enabled)
	globalManagerMu.RLock()
	canaryMgr := globalCanaryManager // Can be nil
	globalManagerMu.RUnlock()

	if canaryMgr != nil {
		// Get active strategy safely
		activeStrategy, strategy := mgr.GetActiveStrategy()
		if strategy != nil {
			activeKeyID := strategy.ActiveKeyID()
			// Note: EnsureCanary has internal do-once logic, safe to call on every encrypt
			if err := canaryMgr.EnsureCanary(ctx, activeStrategy, activeKeyID); err != nil {
				// Log error but don't fail encryption (canary is verification, not critical path)
				globalManagerMu.RLock()
				if globalLogger != nil {
					globalLogger.Errorf("Failed to ensure canary for %s/%s: %v", activeStrategy, activeKeyID, err)
				}
				globalManagerMu.RUnlock()
			}
		}
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
