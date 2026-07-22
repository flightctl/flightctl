package templateversion

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
		handler, _, _, _ := newTestHandler()
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		orgId := uuid.New()
		tv := testTemplateVersion("myfleet", "v1")
		result, status := traced.CreateTemplateVersion(context.Background(), orgId, tv, false)
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, result)

		got, status := traced.GetTemplateVersion(context.Background(), orgId, "myfleet", "v1")
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "v1", *got.Metadata.Name)
	})
}
