package encryption

import (
	"sync"
)

// memoryCanaryStore stores canaries in memory (for testing).
// Thread-safe for concurrent access.
type memoryCanaryStore struct {
	mu       sync.RWMutex
	canaries map[string]*Canary // "strategy/keyID" -> canary
}

// newMemoryCanaryStore creates a new in-memory canary store.
func newMemoryCanaryStore() *memoryCanaryStore {
	return &memoryCanaryStore{
		canaries: make(map[string]*Canary),
	}
}

// Get retrieves a canary for the given strategy and keyID.
func (s *memoryCanaryStore) Get(strategy, keyID string) (*Canary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := strategy + "/" + keyID
	canary, exists := s.canaries[key]
	if !exists {
		return nil, nil
	}

	// Return a deep copy to prevent external mutation
	encryptedCopy := make([]byte, len(canary.EncryptedValue))
	copy(encryptedCopy, canary.EncryptedValue)

	return &Canary{
		Strategy:       canary.Strategy,
		KeyID:          canary.KeyID,
		EncryptedValue: encryptedCopy,
		CreatedAt:      canary.CreatedAt,
	}, nil
}

// Save creates or updates a canary.
func (s *memoryCanaryStore) Save(canary *Canary) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := canary.Strategy + "/" + canary.KeyID

	// Store a deep copy to prevent external mutation
	encryptedCopy := make([]byte, len(canary.EncryptedValue))
	copy(encryptedCopy, canary.EncryptedValue)

	s.canaries[key] = &Canary{
		Strategy:       canary.Strategy,
		KeyID:          canary.KeyID,
		EncryptedValue: encryptedCopy,
		CreatedAt:      canary.CreatedAt,
	}

	return nil
}

// GetAll retrieves all stored canaries.
func (s *memoryCanaryStore) GetAll() ([]Canary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Canary, 0, len(s.canaries))
	for _, canary := range s.canaries {
		// Return deep copies to prevent external mutation
		encryptedCopy := make([]byte, len(canary.EncryptedValue))
		copy(encryptedCopy, canary.EncryptedValue)

		result = append(result, Canary{
			Strategy:       canary.Strategy,
			KeyID:          canary.KeyID,
			EncryptedValue: encryptedCopy,
			CreatedAt:      canary.CreatedAt,
		})
	}

	return result, nil
}
