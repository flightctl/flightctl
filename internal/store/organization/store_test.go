package organization

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewOrganizationStore_ReturnsNonNilStore proves the constructor builds a
// working Store without dialing the database, and (via the package compiling
// at all) that the compile-time conformance check in store.go holds.
func TestNewOrganizationStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewOrganizationStore(nil)

	req.NotNil(s)
}
