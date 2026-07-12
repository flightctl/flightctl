package encryption

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Canary represents an encrypted test value used to verify encryption/decryption works.
// Each canary is specific to a (strategy, keyID) pair.
type Canary struct {
	Strategy       string    `json:"strategy"`        // e.g., "v1"
	KeyID          string    `json:"key_id"`          // e.g., "default"
	EncryptedValue []byte    `json:"encrypted_value"` // e.g., "enc:v1:default:..."
	CreatedAt      time.Time `json:"created_at"`
}

// CanaryStore abstracts canary persistence.
type CanaryStore interface {
	// Get retrieves a canary for the given strategy and keyID.
	// Returns nil if not found.
	Get(strategy, keyID string) (*Canary, error)

	// Save creates or updates a canary.
	Save(canary *Canary) error

	// GetAll retrieves all stored canaries.
	GetAll() ([]Canary, error)
}

// ValidationResult represents the result of validating a single canary.
type ValidationResult struct {
	Strategy string `json:"strategy"`
	KeyID    string `json:"key_id"`
	Status   string `json:"status"` // "ok", "failed", "mismatch"
	Error    string `json:"error,omitempty"`
}

// CanaryManager manages encryption canaries for verification.
// It ensures canaries exist for all active keys and validates they can be decrypted.
type CanaryManager struct {
	encMgr  *Manager
	store   CanaryStore
	mu      sync.RWMutex
	ensured map[string]bool // "strategy/keyID" -> true (do-once per session)
}

// NewCanaryManager creates a new canary manager.
func NewCanaryManager(encMgr *Manager, store CanaryStore) *CanaryManager {
	return &CanaryManager{
		encMgr:  encMgr,
		store:   store,
		ensured: make(map[string]bool),
	}
}

// EnsureCanary ensures a canary exists for the given strategy and keyID.
// This is called on first encryption with a particular key.
// Uses do-once pattern: only checks/creates once per session.
func (cm *CanaryManager) EnsureCanary(ctx context.Context, strategy, keyID string) error {
	key := fmt.Sprintf("%s/%s", strategy, keyID)

	// Fast path: check if already ensured (read lock)
	cm.mu.RLock()
	if cm.ensured[key] {
		cm.mu.RUnlock()
		return nil
	}
	cm.mu.RUnlock()

	// Slow path: need to check storage and potentially create
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine may have created it)
	if cm.ensured[key] {
		return nil
	}

	// Check if canary already exists in storage
	existing, err := cm.store.Get(strategy, keyID)
	if err != nil {
		return fmt.Errorf("check existing canary: %w", err)
	}

	if existing != nil {
		// Canary exists, mark as ensured
		cm.ensured[key] = true
		return nil
	}

	// Create new canary
	plaintext := fmt.Sprintf("flightctl-canary-%s-%s", strategy, keyID)
	encrypted, err := cm.encMgr.Encrypt(ctx, []byte(plaintext))
	if err != nil {
		return fmt.Errorf("encrypt canary: %w", err)
	}

	canary := &Canary{
		Strategy:       strategy,
		KeyID:          keyID,
		EncryptedValue: encrypted,
		CreatedAt:      time.Now(),
	}

	if err := cm.store.Save(canary); err != nil {
		return fmt.Errorf("save canary: %w", err)
	}

	// Mark as ensured
	cm.ensured[key] = true

	return nil
}

// ValidateAll validates all stored canaries.
// Returns a list of validation results, one per canary.
// This is used by health/status endpoints to verify encryption is working.
func (cm *CanaryManager) ValidateAll(ctx context.Context) ([]ValidationResult, error) {
	canaries, err := cm.store.GetAll()
	if err != nil {
		return nil, fmt.Errorf("get all canaries: %w", err)
	}

	results := make([]ValidationResult, 0, len(canaries))
	for _, canary := range canaries {
		result := cm.validateOne(ctx, &canary)
		results = append(results, result)
	}

	return results, nil
}

// validateOne validates a single canary.
func (cm *CanaryManager) validateOne(ctx context.Context, canary *Canary) ValidationResult {
	ctx, span := startCanaryValidateSpan(ctx, canary.Strategy, canary.KeyID)
	defer span.End()

	expected := fmt.Sprintf("flightctl-canary-%s-%s", canary.Strategy, canary.KeyID)

	decrypted, err := cm.encMgr.Decrypt(ctx, canary.EncryptedValue)
	if err != nil {
		// err is already wrapped with sentinel error from Decrypt()
		recordError(span, err)
		return ValidationResult{
			Strategy: canary.Strategy,
			KeyID:    canary.KeyID,
			Status:   "failed",
			Error:    fmt.Sprintf("decrypt failed: %v", err),
		}
	}

	if string(decrypted) != expected {
		// Mismatch is an error condition
		recordError(span, ErrDecryptionFailed)
		return ValidationResult{
			Strategy: canary.Strategy,
			KeyID:    canary.KeyID,
			Status:   "mismatch",
			Error:    fmt.Sprintf("expected %q, got %q", expected, string(decrypted)),
		}
	}

	recordSuccess(span)
	return ValidationResult{
		Strategy: canary.Strategy,
		KeyID:    canary.KeyID,
		Status:   "ok",
	}
}

// GetActiveCanary returns the canary for the currently active strategy/key.
// Returns nil if not found.
func (cm *CanaryManager) GetActiveCanary(ctx context.Context) (*Canary, error) {
	version, strategy := cm.encMgr.GetActiveStrategy()
	if version == "" {
		return nil, fmt.Errorf("no active strategy set")
	}
	if strategy == nil {
		return nil, fmt.Errorf("active strategy %s not found", version)
	}

	keyID := strategy.ActiveKeyID()
	return cm.store.Get(version, keyID)
}
