package checkpoint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrapWithTracing(t *testing.T) {
	t.Run("When inner is nil it should return nil", func(t *testing.T) {
		require.Nil(t, WrapWithTracing(nil))
	})

	t.Run("When inner is non-nil it should delegate calls and return the result unchanged", func(t *testing.T) {
		fakeStore := newFakeCheckpointStore()
		handler := NewServiceHandler(fakeStore)
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		status := traced.SetCheckpoint(context.Background(), "c1", "k1", []byte("v1"))
		require.Equal(t, int32(200), status.Code)

		value, status := traced.GetCheckpoint(context.Background(), "c1", "k1")
		require.Equal(t, int32(200), status.Code)
		require.Equal(t, []byte("v1"), value)
	})
}
