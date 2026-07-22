package enrollmentrequest

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCallbackEnrollmentRequestUpdated(t *testing.T) {
	t.Run("When err is non-nil it should emit a failure event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler(t)
		h.callbackEnrollmentRequestUpdated(context.Background(), domain.EnrollmentRequestKind, uuid.New(), "er1", nil, nil, true, errors.New("boom"))
		require.Len(t, ev.createdEvents, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, ev.createdEvents[0].Reason)
	})

	t.Run("When created it should emit a resource-created event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler(t)
		er := &domain.EnrollmentRequest{Metadata: domain.ObjectMeta{Name: lo.ToPtr("er1")}}
		h.callbackEnrollmentRequestUpdated(context.Background(), domain.EnrollmentRequestKind, uuid.New(), "er1", nil, er, true, nil)
		require.Len(t, ev.createdEvents, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.createdEvents[0].Reason)
	})
}

func TestCallbackEnrollmentRequestApproved(t *testing.T) {
	t.Run("When err is nil it should emit an approved event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler(t)
		h.callbackEnrollmentRequestApproved(context.Background(), domain.EnrollmentRequestKind, uuid.New(), "er1", nil, nil, false, nil)
		require.Len(t, ev.createdEvents, 1)
		require.Equal(t, domain.EventReasonEnrollmentRequestApproved, ev.createdEvents[0].Reason)
	})

	t.Run("When err is non-nil it should emit an approval-failed event", func(t *testing.T) {
		h, _, _, _, ev := newTestHandler(t)
		h.callbackEnrollmentRequestApproved(context.Background(), domain.EnrollmentRequestKind, uuid.New(), "er1", nil, nil, false, errors.New("boom"))
		require.Len(t, ev.createdEvents, 1)
		require.Equal(t, domain.EventReasonEnrollmentRequestApprovalFailed, ev.createdEvents[0].Reason)
	})
}
