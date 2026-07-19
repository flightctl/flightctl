package enrollmentrequest

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
		handler, fakeStore, _, _, _ := newTestHandler(t)
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		orgId := uuid.New()
		er := testEnrollmentRequest("foo")
		fakeStore.items["foo"] = &er

		got, status := traced.GetEnrollmentRequest(context.Background(), orgId, "foo")
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "foo", *got.Metadata.Name)
	})
}
