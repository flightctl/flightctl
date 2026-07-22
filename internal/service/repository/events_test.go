package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCallbackRepositoryUpdated(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		h.callbackRepositoryUpdated(context.Background(), domain.RepositoryKind, uuid.New(), "r1", nil, nil, false, errors.New("boom"))
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceUpdateFailed, ev.created[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		repo := &domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("r1")}}
		h.callbackRepositoryUpdated(context.Background(), domain.RepositoryKind, uuid.New(), "r1", nil, repo, true, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})

	t.Run("When the Accessible condition becomes true it should emit a RepositoryAccessible event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		name := "r1"
		oldRepo := &domain.Repository{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.RepositoryStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeRepositoryAccessible, Status: domain.ConditionStatusFalse},
			}},
		}
		newRepo := &domain.Repository{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.RepositoryStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeRepositoryAccessible, Status: domain.ConditionStatusTrue},
			}},
		}
		h.callbackRepositoryUpdated(context.Background(), domain.RepositoryKind, uuid.New(), name, oldRepo, newRepo, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonRepositoryAccessible)
	})
}
