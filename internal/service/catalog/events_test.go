package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCallbackCatalogUpdated(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		h.callbackCatalogUpdated(context.Background(), domain.CatalogKind, uuid.New(), "cat1", nil, nil, true, errors.New("boom"))
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, ev.created[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		cat := &domain.Catalog{Metadata: domain.ObjectMeta{Name: lo.ToPtr("cat1")}}
		h.callbackCatalogUpdated(context.Background(), domain.CatalogKind, uuid.New(), "cat1", nil, cat, true, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})
}
