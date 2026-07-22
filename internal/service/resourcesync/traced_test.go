package resourcesync

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
		handler, _, _, _, _ := newTestHandler()
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		orgId := uuid.New()
		rs := testResourceSync("foo")
		result, status := traced.CreateResourceSync(context.Background(), orgId, rs)
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, result)

		got, status := traced.GetResourceSync(context.Background(), orgId, "foo")
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "foo", *got.Metadata.Name)
	})
}
