package fleet

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestNewFleetStore_ReturnsNonNilStore proves the constructor builds a
// working Store without dialing the database. This also exercises the
// cross-package store.NewGenericStore[...] wiring, proving it compiles and
// runs at construction time with a nil DB handle.
func TestNewFleetStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewFleetStore(nil, logrus.New())

	req.NotNil(s)
}
