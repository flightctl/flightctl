package catalog

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWrapWithTracing(t *testing.T) {
	t.Run("When inner is nil it should return nil", func(t *testing.T) {
		require.Nil(t, WrapWithTracing(nil))
	})

	t.Run("When inner is non-nil it should delegate calls and return the result unchanged", func(t *testing.T) {
		handler, _, _ := newTestHandler()
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		_, status := traced.GetCatalog(context.Background(), uuid.New(), "missing")
		require.Equal(t, domain.StatusResourceNotFound(domain.CatalogKind, "missing"), status)
	})
}
