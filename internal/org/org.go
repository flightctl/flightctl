package org

import (
	"fmt"

	"github.com/google/uuid"
)

// DefaultID is the well-known UUID reserved for the default / system organization.
// It is equivalent to the previous `store.NullOrgId` constant.
var DefaultID = uuid.MustParse("00000000-0000-0000-0000-000000000000")

// Parse validates that the supplied string is a valid UUID and returns it.
func Parse(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid organization ID %q: %w", s, err)
	}
	return id, nil
}

// MustParse is like Parse but panics on error (to be used with hard-coded strings).
func MustParse(s string) uuid.UUID {
	id, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}
