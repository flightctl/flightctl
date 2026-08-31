package delta

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
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

func TestRejectedConflictAllowsStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{"When failed it should allow rejected overwrite", model.DeltaGenerationFailed, true},
		{"When rejected it should allow size refresh", model.DeltaGenerationRejected, true},
		{"When pending it should allow rejected overwrite", model.DeltaGenerationPending, true},
		{"When in_progress it should not allow overwrite", model.DeltaGenerationInProgress, false},
		{"When succeeded it should not allow overwrite", model.DeltaGenerationSucceeded, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, rejectedConflictAllows(tt.status))
		})
	}
}

func TestIsClaimableStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{"When pending it should be claimable", model.DeltaGenerationPending, true},
		{"When in_progress it should not be claimable", model.DeltaGenerationInProgress, false},
		{"When succeeded it should not be claimable", model.DeltaGenerationSucceeded, false},
		{"When failed it should not be claimable", model.DeltaGenerationFailed, false},
		{"When rejected it should not be claimable", model.DeltaGenerationRejected, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isClaimableStatus(tt.status))
		})
	}
}
