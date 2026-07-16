package authprovider

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCallbackAuthProviderUpdated(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		h.callbackAuthProviderUpdated(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", nil, nil, true, errors.New("boom"))
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, ev.created[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		ap := &domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("ap1")}}
		h.callbackAuthProviderUpdated(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", nil, ap, true, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})

	t.Run("When updated with metadata changes it should emit a resource-updated event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		oldAP := &domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("ap1"), Generation: lo.ToPtr(int64(1))}}
		newAP := &domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("ap1"), Generation: lo.ToPtr(int64(2))}}
		h.callbackAuthProviderUpdated(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", oldAP, newAP, false, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceUpdated, ev.created[0].Reason)
	})

	t.Run("When updated with no metadata changes it should not emit an event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		ap := &domain.AuthProvider{Metadata: domain.ObjectMeta{Name: lo.ToPtr("ap1"), Generation: lo.ToPtr(int64(1))}}
		h.callbackAuthProviderUpdated(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", ap, ap, false, nil)
		require.Empty(t, ev.created)
	})
}

func TestCallbackAuthProviderDeleted(t *testing.T) {
	t.Run("When err is nil it should emit a deletion-success event", func(t *testing.T) {
		h, _, ev := newTestHandler()
		h.callbackAuthProviderDeleted(context.Background(), domain.AuthProviderKind, uuid.New(), "ap1", nil, nil, false, nil)
		require.Len(t, ev.deleted, 1)
	})
}
