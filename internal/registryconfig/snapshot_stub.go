package registryconfig

import "github.com/google/uuid"

// Snapshot is a placeholder for registry selection and endpoint composition.
// It delivers no contributions until the registry selection provider is connected.
type Snapshot struct{}

func NewSnapshot(_ uuid.UUID, _ []Candidate) (*Snapshot, error) {
	return &Snapshot{}, nil
}

func (s *Snapshot) Compose(_ []PullNeed) (*Composition, error) {
	return &Composition{}, nil
}
