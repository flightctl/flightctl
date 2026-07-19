package event

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestNewEventStore_ReturnsNonNilStore proves the constructor builds a
// working Store without dialing the database. This also exercises the
// cross-package store.NewGenericStore[...] wiring, proving it compiles and
// runs at construction time with a nil DB handle.
func TestNewEventStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewEventStore(nil, logrus.New())

	req.NotNil(s)
}
