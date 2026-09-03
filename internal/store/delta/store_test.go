package delta

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestNewStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewStore(nil, logrus.New())

	req.NotNil(s)
}

func TestListWaitingPastDeadline_WhenLimitIsOutOfRangeItShouldError(t *testing.T) {
	t.Parallel()
	s := NewStore(nil, logrus.New())
	ctx := context.Background()

	now := time.Now()
	_, err := s.ListWaitingPastDeadline(ctx, 0, now)
	require.Error(t, err)

	_, err = s.ListWaitingPastDeadline(ctx, MaxListWaitingPastDeadline+1, now)
	require.Error(t, err)
}
