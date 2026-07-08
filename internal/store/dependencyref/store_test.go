package dependencyref

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestNewDependencyRefStore_ReturnsNonNilStore proves the constructor builds
// a working Store without dialing the database, and (via the package
// compiling at all) that the compile-time conformance check in store.go
// holds.
func TestNewDependencyRefStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewDependencyRefStore(nil, logrus.New())

	req.NotNil(s)
}
