package resourcesync

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCallbackResourceSyncUpdated(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler()
		h.callbackResourceSyncUpdated(context.Background(), domain.ResourceSyncKind, uuid.New(), "rs1", nil, nil, true, errors.New("boom"))
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, ev.created[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler()
		rs := &domain.ResourceSync{Metadata: domain.ObjectMeta{Name: lo.ToPtr("rs1")}}
		h.callbackResourceSyncUpdated(context.Background(), domain.ResourceSyncKind, uuid.New(), "rs1", nil, rs, true, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})

	t.Run("When the commit hash changes it should emit a commit-detected event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler()
		name := "rs1"
		oldRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status:   &domain.ResourceSyncStatus{ObservedCommit: lo.ToPtr("aaa")},
		}
		newRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status:   &domain.ResourceSyncStatus{ObservedCommit: lo.ToPtr("bbb")},
		}
		h.callbackResourceSyncUpdated(context.Background(), domain.ResourceSyncKind, uuid.New(), name, oldRs, newRs, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonResourceSyncCommitDetected)
	})

	t.Run("When the Synced condition transitions to true it should emit a synced event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler()
		name := "rs1"
		oldRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.ResourceSyncStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusFalse},
			}},
		}
		newRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.ResourceSyncStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusTrue},
			}},
		}
		h.callbackResourceSyncUpdated(context.Background(), domain.ResourceSyncKind, uuid.New(), name, oldRs, newRs, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonResourceSyncSynced)
	})

	t.Run("When the Synced condition fails (not NewHashDetected) it should emit a sync-failed event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler()
		name := "rs1"
		oldRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.ResourceSyncStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusTrue},
			}},
		}
		newRs := &domain.ResourceSync{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.ResourceSyncStatus{Conditions: []domain.Condition{
				{Type: domain.ConditionTypeResourceSyncSynced, Status: domain.ConditionStatusFalse, Reason: "SomeFailure", Message: "it broke"},
			}},
		}
		h.callbackResourceSyncUpdated(context.Background(), domain.ResourceSyncKind, uuid.New(), name, oldRs, newRs, false, nil)
		var reasons []domain.EventReason
		for _, e := range ev.created {
			reasons = append(reasons, e.Reason)
		}
		require.Contains(t, reasons, domain.EventReasonResourceSyncSyncFailed)
	})
}
