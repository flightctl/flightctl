package delta

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestNewStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewStore(nil, logrus.New())

	req.NotNil(s)
}
