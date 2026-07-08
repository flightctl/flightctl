package encryption

import (
	"context"
	"fmt"
	"sort"
)

// EncryptionStatus represents the complete encryption system state
type EncryptionStatus struct {
	Enabled             bool        `json:"enabled"`
	Active              *ActiveInfo `json:"active,omitempty"`
	CanaryChecksEnabled bool        `json:"canary_checks_enabled"`
	Keys                []KeyStatus `json:"keys"`
}

// ActiveInfo describes the currently active encryption configuration
type ActiveInfo struct {
	Strategy  string `json:"strategy"`  // "v1"
	KeyID     string `json:"key_id"`    // "default"
	Algorithm string `json:"algorithm"` // "AES-256-GCM"
}

// KeyStatus represents a single encryption key and its health
type KeyStatus struct {
	Strategy     string `json:"strategy"`      // "v1"
	KeyID        string `json:"key_id"`        // "default"
	Configured   bool   `json:"configured"`    // Is this key currently configured in the strategy?
	Active       bool   `json:"active"`        // Is this the active strategy+key?
	CanaryStatus string `json:"canary_status"` // "ok", "failed", "not_checked", "key_missing"
}

// Status returns the complete encryption system status.
// Shows the union of:
// - All configured keys from the active strategy (always shown)
// - All canaries from any strategy (only if canary exists)
// Safe to call even if encryption is not initialized (returns Enabled: false).
func Status(ctx context.Context) (*EncryptionStatus, error) {
	mgr := GlobalManager()
	if mgr == nil {
		return &EncryptionStatus{
			Enabled:             false,
			CanaryChecksEnabled: false,
			Keys:                []KeyStatus{},
		}, nil
	}

	// Get active strategy info
	activeStrategy, activeStrat := mgr.GetActiveStrategy()
	if activeStrat == nil {
		return nil, fmt.Errorf("active strategy %s not found", activeStrategy)
	}

	activeKeyID := activeStrat.ActiveKeyID()

	status := &EncryptionStatus{
		Enabled: true,
		Active: &ActiveInfo{
			Strategy:  activeStrategy,
			KeyID:     activeKeyID,
			Algorithm: activeStrat.Algorithm(),
		},
	}

	// Get canary manager (can be nil)
	canaryMgr := GlobalCanaryManager()
	status.CanaryChecksEnabled = (canaryMgr != nil)

	// Build union of configured keys + canaries
	keyMap := make(map[string]*KeyStatus) // key = "strategy/keyID"

	// Step 1: Add all configured keys from active strategy
	configuredKeys := activeStrat.ConfiguredKeys()
	for _, keyID := range configuredKeys {
		key := fmt.Sprintf("%s/%s", activeStrategy, keyID)
		keyMap[key] = &KeyStatus{
			Strategy:     activeStrategy,
			KeyID:        keyID,
			Configured:   true,
			Active:       (keyID == activeKeyID),
			CanaryStatus: "not_checked", // Default for canaries disabled or no canary yet
		}
	}

	// Step 2: If canaries enabled, merge canary results
	if canaryMgr != nil {
		results, err := canaryMgr.ValidateAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("validate canaries: %w", err)
		}

		for _, result := range results {
			key := fmt.Sprintf("%s/%s", result.Strategy, result.KeyID)

			if entry, exists := keyMap[key]; exists {
				// Key is already in map (from active strategy) - update canary status
				entry.CanaryStatus = result.Status
			} else {
				// Canary from inactive strategy - check if key still configured
				isConfigured := false
				strategy, stratExists := mgr.GetStrategy(result.Strategy)

				if stratExists {
					for _, configuredKey := range strategy.ConfiguredKeys() {
						if configuredKey == result.KeyID {
							isConfigured = true
							break
						}
					}
				}

				// Determine canary status
				canaryStatus := result.Status
				if !isConfigured {
					// Key removed but canary exists - potential data loss
					canaryStatus = "key_missing"
				}

				keyMap[key] = &KeyStatus{
					Strategy:     result.Strategy,
					KeyID:        result.KeyID,
					Configured:   isConfigured,
					Active:       false, // Not active (different strategy or key)
					CanaryStatus: canaryStatus,
				}
			}
		}
	}

	// Convert map to slice and sort for deterministic output
	status.Keys = make([]KeyStatus, 0, len(keyMap))
	for _, entry := range keyMap {
		status.Keys = append(status.Keys, *entry)
	}
	sort.Slice(status.Keys, func(i, j int) bool {
		if status.Keys[i].Strategy != status.Keys[j].Strategy {
			return status.Keys[i].Strategy < status.Keys[j].Strategy
		}
		return status.Keys[i].KeyID < status.Keys[j].KeyID
	})

	return status, nil
}
