package templateversion

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCallbackTemplateVersionUpdated(t *testing.T) {
	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, _, _, ev := newTestHandler()
		tv := &domain.TemplateVersion{Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv1")}}
		h.callbackTemplateVersionUpdated(context.Background(), domain.TemplateVersionKind, uuid.New(), "tv1", nil, tv, true, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})
}
