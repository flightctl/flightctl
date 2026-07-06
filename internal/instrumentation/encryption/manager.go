package encryption

import (
	"context"
	"fmt"
	"strings"

	"github.com/stoewer/go-strcase"
)

// NewManager creates a new encryption manager with no strategies registered.
func NewManager() *Manager {
	return &Manager{
		strategies: make(map[string]Strategy),
	}
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
		return fmt.Errorf("strategy version %q not registered", normalized)
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
		return nil, fmt.Errorf("no active encryption strategy set")
	}
	if !exists {
		return nil, fmt.Errorf("active strategy %q not found", activeStrategy)
	}

	body, err := strategy.EncryptPlaintext(ctx, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt with strategy %s: %w", activeStrategy, err)
	}

	// Prefix format: enc:<version>:<encrypted-payload>
	prefixed := fmt.Sprintf("enc:%s:%s", strategy.Version(), string(body))
	return []byte(prefixed), nil
}

// ProcessEncryption intelligently handles data that may be plaintext, encrypted with current
// version/key, or encrypted with old version/key. Behavior:
// 1. Plaintext: encrypts with active strategy
// 2. Same version/key: returns unchanged (avoids wasteful re-encryption)
// 3. Different version: migrates to active version
// 4. Same version, different key: re-encrypts with active key
func (m *Manager) ProcessEncryption(ctx context.Context, data []byte) ([]byte, error) {
	// Not encrypted - encrypt it
	currentVersion, body, ok := parseEncryptedFormat(data)
	if !ok {
		return m.Encrypt(ctx, data)
	}

	m.mu.RLock()
	activeStrategyVersion := m.activeStrategy
	currentStrategy, currentExists := m.strategies[currentVersion]
	activeStrategy, activeExists := m.strategies[activeStrategyVersion]
	m.mu.RUnlock()

	if !currentExists {
		return nil, fmt.Errorf("strategy %s not found", currentVersion)
	}
	if !activeExists {
		return nil, fmt.Errorf("active strategy %s not found", activeStrategyVersion)
	}

	// Parse strategy body to extract keyID
	parsed, err := currentStrategy.ParseBody(body)
	if err != nil {
		return nil, fmt.Errorf("parse %s body: %w", currentVersion, err)
	}

	// Different version - decrypt and migrate
	if currentVersion != activeStrategyVersion {
		plaintext, err := currentStrategy.DecryptParsed(ctx, parsed)
		if err != nil {
			return nil, fmt.Errorf("decrypt for version migration %s to %s: %w", currentVersion, activeStrategyVersion, err)
		}
		return m.Encrypt(ctx, plaintext)
	}

	// Same version/key - return unchanged
	if parsed.KeyID == activeStrategy.ActiveKeyID() {
		return data, nil
	}

	// Same version, different key - decrypt and re-encrypt
	plaintext, err := currentStrategy.DecryptParsed(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("decrypt for key rotation %s to %s: %w", parsed.KeyID, activeStrategy.ActiveKeyID(), err)
	}

	return m.Encrypt(ctx, plaintext)
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
		return nil, fmt.Errorf("invalid encrypted format: expected enc:<version>:<data>, got %q", str)
	}

	// Route to the correct strategy based on version
	m.mu.RLock()
	strategy, exists := m.strategies[version]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("no decryption strategy registered for version %q", version)
	}

	// Parse and decrypt
	parsed, err := strategy.ParseBody(body)
	if err != nil {
		return nil, fmt.Errorf("parse %s body: %w", version, err)
	}

	plaintext, err := strategy.DecryptParsed(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("decrypt with strategy %s: %w", version, err)
	}

	return plaintext, nil
}
