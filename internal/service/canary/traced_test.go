package canary

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/stretchr/testify/require"
)

func TestWrapWithTracing(t *testing.T) {
	t.Run("When inner is nil it should return nil", func(t *testing.T) {
		require.Nil(t, WrapWithTracing(nil))
	})

	t.Run("When inner is non-nil it should delegate calls and return the result unchanged", func(t *testing.T) {
		fake := newFakeCanaryStore()
		handler := NewServiceHandler(fake)
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		status := traced.Save(context.Background(), &encryption.Canary{
			Strategy:       "v1",
			KeyID:          "key1",
			EncryptedValue: []byte("enc-value"),
		})
		require.Equal(t, int32(http.StatusOK), status.Code)

		canary, status := traced.Get(context.Background(), "v1", "key1")
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.NotNil(t, canary)
		require.Equal(t, "v1", canary.Strategy)

		all, status := traced.GetAll(context.Background())
		require.Equal(t, int32(http.StatusOK), status.Code)
		require.Len(t, all, 1)
	})
}
