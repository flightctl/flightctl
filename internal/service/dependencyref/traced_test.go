package dependencyref

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWrapWithTracing(t *testing.T) {
	t.Run("When inner is nil it should return nil", func(t *testing.T) {
		require.Nil(t, WrapWithTracing(nil))
	})

	t.Run("When inner is non-nil it should delegate calls and return the result unchanged", func(t *testing.T) {
		h, store := newTestHandler()
		traced := WrapWithTracing(h)
		require.NotNil(t, traced)

		status := traced.DeleteDependencyRefsByFleet(context.Background(), uuid.New(), "fleet1")
		require.Equal(t, int32(200), status.Code)
		require.Equal(t, "fleet1", store.deletedFleet)
	})
}
