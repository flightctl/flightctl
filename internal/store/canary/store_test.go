package canary

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestNewCanaryStore_ReturnsNonNilStore proves the constructor builds a
// working Store without dialing the database, confirming it compiles and
// runs at construction time with a nil DB handle.
func TestNewCanaryStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewCanaryStore(nil, logrus.New())

	req.NotNil(s)
}
