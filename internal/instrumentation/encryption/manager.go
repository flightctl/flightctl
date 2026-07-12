package encryption

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stoewer/go-strcase"
)

// NewManager creates a new encryption manager with no strategies registered.
func NewManager() *Manager {
	return &Manager{
		strategies: make(map[string]Strategy),
	}
}

// SetMetricsRecorder sets the metrics recorder for this manager.
// This is optional - if not set, no metrics will be recorded.
func (m *Manager) SetMetricsRecorder(metrics MetricsRecorder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.metrics = metrics
}

// GetActiveStrategy returns the active strategy version and the strategy itself.
// Returns ("", nil) if no active strategy is set.
func (m *Manager) GetActiveStrategy() (version string, strategy Strategy) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	version = m.activeStrategy
	if version != "" {
		strategy = m.strategies[version]
	}
	return version, strategy
}

// ActiveStrategyVersion returns just the active strategy version string.
// Returns empty string if no active strategy is set.
func (m *Manager) ActiveStrategyVersion() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeStrategy
}

// GetStrategy returns the strategy for the given version.
// The version is automatically normalized to kebab-case.
// Returns (nil, false) if the strategy is not registered.
func (m *Manager) GetStrategy(version string) (Strategy, bool) {
	normalized := strcase.KebabCase(version)
	m.mu.RLock()
	defer m.mu.RUnlock()

	strategy, exists := m.strategies[normalized]
	return strategy, exists
}

// StrategyCount returns the number of registered strategies.
func (m *Manager) StrategyCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.strategies)
}

// RegisterStrategy adds an encryption strategy to the manager.
// The version identifier is automatically normalized to kebab-case.
// If setActive is true, this strategy becomes the active strategy for new encryptions.
func (m *Manager) RegisterStrategy(s Strategy, setActive bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	version := strcase.KebabCase(s.Version())
	m.strategies[version] = s
	if setActive {
		m.activeStrategy = version
	}
}

// SetActiveStrategy selects which strategy will be used for new encryptions.
// The version is automatically normalized to kebab-case.
func (m *Manager) SetActiveStrategy(version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	normalized := strcase.KebabCase(version)
	if _, exists := m.strategies[normalized]; !exists {
		return fmt.Errorf("%w: strategy version %q", ErrStrategyNotFound, normalized)
	}
	m.activeStrategy = normalized
	return nil
}

// Encrypt encrypts plaintext using the active strategy.
// Returns encrypted data with format: enc:<version>:<strategy-specific-data>
// Returns error if input is already encrypted (use ProcessEncryption for that).
func (m *Manager) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	if IsEncrypted(plaintext) {
		return nil, fmt.Errorf("Encrypt expects plaintext, got already-encrypted data (use ProcessEncryption instead)")
	}

	m.mu.RLock()
	activeStrategy := m.activeStrategy
	strategy, exists := m.strategies[activeStrategy]
	m.mu.RUnlock()

	if activeStrategy == "" {
		return nil, ErrNoActiveStrategy
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrStrategyNotFound, activeStrategy)
	}

	activeKeyID := strategy.ActiveKeyID()
	ctx, span := startEncryptSpan(ctx, activeStrategy, activeKeyID)
	defer span.End()

	start := time.Now()
	body, err := strategy.EncryptPlaintext(ctx, plaintext)
	duration := time.Since(start)

	if err != nil {
		wrappedErr := fmt.Errorf("%w: %v", ErrEncryptionFailed, err)
		recordError(span, wrappedErr)
		// Record error metrics
		if m.metrics != nil {
			m.metrics.RecordOperation("encrypt", activeStrategy, activeKeyID, "error", duration)
			m.metrics.RecordError("encrypt", activeStrategy, activeKeyID, CategorizeError(wrappedErr))
		}
		return nil, wrappedErr
	}

	// Prefix format: enc:<version>:<encrypted-payload>
	prefixed := fmt.Sprintf("enc:%s:%s", strategy.Version(), string(body))
	recordSuccess(span)
	// Record success metrics
	if m.metrics != nil {
		m.metrics.RecordOperation("encrypt", activeStrategy, activeKeyID, "success", duration)
	}
	return []byte(prefixed), nil
}

// ProcessEncryption intelligently handles data that may be plaintext, encrypted with current
// version/key, or encrypted with old version/key. Behavior:
// 1. Plaintext: encrypts with active strategy
// 2. Same version/key: returns unchanged (avoids wasteful re-encryption)
// 3. Different version: migrates to active version
// 4. Same version, different key: re-encrypts with active key
func (m *Manager) ProcessEncryption(ctx context.Context, data []byte) ([]byte, error) {
	ctx, span := startProcessSpan(ctx)
	defer span.End()

	// Not encrypted - encrypt it
	currentVersion, body, ok := parseEncryptedFormat(data)
	if !ok {
		recordProcessAction(span, actionEncryptPlaintext)
		result, err := m.Encrypt(ctx, data)
		if err != nil {
			recordError(span, err)
			return nil, err
		}
		recordSuccess(span)
		return result, nil
	}

	m.mu.RLock()
	activeStrategyVersion := m.activeStrategy
	currentStrategy, currentExists := m.strategies[currentVersion]
	activeStrategy, activeExists := m.strategies[activeStrategyVersion]
	m.mu.RUnlock()

	if !currentExists {
		err := fmt.Errorf("%w: %s", ErrStrategyNotFound, currentVersion)
		recordError(span, err)
		return nil, err
	}
	if !activeExists {
		err := fmt.Errorf("%w: %s", ErrStrategyNotFound, activeStrategyVersion)
		recordError(span, err)
		return nil, err
	}

	// Parse strategy body to extract keyID
	parsed, err := currentStrategy.ParseBody(body)
	if err != nil {
		wrappedErr := fmt.Errorf("%w: %v", ErrParseFailed, err)
		recordError(span, wrappedErr)
		return nil, wrappedErr
	}

	// Different version - decrypt and migrate
	if currentVersion != activeStrategyVersion {
		recordProcessAction(span, actionReencrypt)
		plaintext, err := currentStrategy.DecryptParsed(ctx, parsed)
		if err != nil {
			wrappedErr := fmt.Errorf("%w: decrypt for version migration %s to %s: %v", ErrDecryptionFailed, currentVersion, activeStrategyVersion, err)
			recordError(span, wrappedErr)
			return nil, wrappedErr
		}
		result, err := m.Encrypt(ctx, plaintext)
		if err != nil {
			// Already wrapped by Encrypt()
			recordError(span, err)
			return nil, err
		}
		recordSuccess(span)
		return result, nil
	}

	// Same version/key - return unchanged
	if parsed.KeyID == activeStrategy.ActiveKeyID() {
		recordProcessAction(span, actionUnchanged)
		recordSuccess(span)
		return data, nil
	}

	// Same version, different key - decrypt and re-encrypt
	recordProcessAction(span, actionReencrypt)
	plaintext, err := currentStrategy.DecryptParsed(ctx, parsed)
	if err != nil {
		wrappedErr := fmt.Errorf("%w: decrypt for key rotation %s to %s: %v", ErrDecryptionFailed, parsed.KeyID, activeStrategy.ActiveKeyID(), err)
		recordError(span, wrappedErr)
		return nil, wrappedErr
	}

	result, err := m.Encrypt(ctx, plaintext)
	if err != nil {
		// Already wrapped by Encrypt()
		recordError(span, err)
		return nil, err
	}
	recordSuccess(span)
	return result, nil
}

// Decrypt decrypts data by detecting the version prefix and routing to the appropriate strategy.
// Supports plaintext passthrough (no prefix) for backward compatibility during migration.
func (m *Manager) Decrypt(ctx context.Context, data []byte) ([]byte, error) {
	str := string(data)

	// Backward compatibility: if no "enc:" prefix, assume plaintext
	if !strings.HasPrefix(str, "enc:") {
		return data, nil
	}

	// Has "enc:" prefix - must be valid encrypted format
	version, body, ok := parseEncryptedFormat(data)
	if !ok {
		return nil, fmt.Errorf("%w: expected enc:<version>:<data>", ErrInvalidFormat)
	}

	// Route to the correct strategy based on version
	m.mu.RLock()
	strategy, exists := m.strategies[version]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrStrategyNotFound, version)
	}

	// Parse and decrypt
	parsed, err := strategy.ParseBody(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	ctx, span := startDecryptSpan(ctx, version, parsed.KeyID)
	defer span.End()

	start := time.Now()
	plaintext, err := strategy.DecryptParsed(ctx, parsed)
	duration := time.Since(start)

	if err != nil {
		wrappedErr := fmt.Errorf("%w: %v", ErrDecryptionFailed, err)
		recordError(span, wrappedErr)
		// Record error metrics
		if m.metrics != nil {
			m.metrics.RecordOperation("decrypt", version, parsed.KeyID, "error", duration)
			m.metrics.RecordError("decrypt", version, parsed.KeyID, CategorizeError(wrappedErr))
		}
		return nil, wrappedErr
	}

	recordSuccess(span)
	// Record success metrics
	if m.metrics != nil {
		m.metrics.RecordOperation("decrypt", version, parsed.KeyID, "success", duration)
	}
	return plaintext, nil
}
