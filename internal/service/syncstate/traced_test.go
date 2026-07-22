package syncstate

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWrapWithTracing(t *testing.T) {
	t.Run("When inner is nil it should return nil", func(t *testing.T) {
		require.Nil(t, WrapWithTracing(nil))
	})

	t.Run("When inner is non-nil it should delegate calls and return the result unchanged", func(t *testing.T) {
		fakeStore := newFakeSyncStateStore()
		handler := NewServiceHandler(fakeStore)
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		orgId := uuid.New()
		status := traced.SetSyncState(context.Background(), orgId, &model.SyncState{ResourceKey: "r1"})
		require.Equal(t, int32(200), status.Code)

		state, status := traced.GetSyncState(context.Background(), orgId, "r1")
		require.Equal(t, int32(200), status.Code)
		require.Equal(t, "r1", state.ResourceKey)
	})
}
