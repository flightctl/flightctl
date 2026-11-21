package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	enrollmentservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type recordingEnrollmentReevaluator struct {
	enrollmentservice.Service
	orgID uuid.UUID
	event domain.Event
	err   error
	calls int
}

func (recorder *recordingEnrollmentReevaluator) ReevaluateEnrollment(_ context.Context, orgID uuid.UUID, event domain.Event) error {
	recorder.orgID, recorder.event = orgID, event
	recorder.calls++
	return recorder.err
}

func TestEnrollmentAdmissionDispatch(t *testing.T) {
	for _, kind := range []string{domain.EnrollmentRequestKind, domain.EnrollmentApprovalPolicyKind} {
		for _, reason := range []domain.EventReason{domain.EventReasonResourceCreated, domain.EventReasonResourceUpdated, domain.EventReasonResourceDeleted} {
			t.Run("When dispatching "+kind+" "+string(reason)+" it should preserve scope and acknowledge after reevaluation", func(t *testing.T) {
				recorder := &recordingEnrollmentReevaluator{}
				orgID := uuid.New()
				event := domain.Event{InvolvedObject: domain.ObjectReference{Kind: kind, Name: "request"}, Reason: reason}
				payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: orgID, Event: event})
				require.NoError(t, err)
				expected := 1
				if kind == domain.EnrollmentRequestKind && reason == domain.EventReasonResourceDeleted {
					expected = 0
				}
				consumer := &MockConsumer{}
				consumer.On("Complete", mock.Anything, "entry", payload, nil).Run(func(mock.Arguments) {
					require.Equal(t, expected, recorder.calls)
				}).Return(nil).Once()
				err = (TaskConsumer{EnrollmentRequestSvc: recorder}).dispatch()(context.Background(), payload, "entry", consumer, logrus.New())
				require.NoError(t, err)
				if expected != 0 {
					require.Equal(t, orgID, recorder.orgID)
					require.Equal(t, event, recorder.event)
				}
				consumer.AssertExpectations(t)
			})
		}
	}
}

func TestEnrollmentAdmissionDispatchRetriesFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	orgID := uuid.New()
	event := domain.Event{InvolvedObject: domain.ObjectReference{Kind: domain.EnrollmentRequestKind, Name: "request"}, Reason: domain.EventReasonResourceUpdated}
	payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: orgID, Event: event})
	require.NoError(t, err)
	recorder := &recordingEnrollmentReevaluator{err: errors.New("admission persistence unavailable")}
	events := eventservice.NewMockService(ctrl)
	events.EXPECT().CreateEvent(gomock.Any(), orgID, gomock.Any()).Times(1)
	consumer := &MockConsumer{}
	consumer.On("Complete", mock.Anything, "entry", payload, mock.Anything).Run(func(args mock.Arguments) {
		require.Equal(t, 1, recorder.calls)
		require.ErrorContains(t, args.Get(3).(error), "admission persistence unavailable")
	}).Return(nil).Once()
	err = (TaskConsumer{EnrollmentRequestSvc: recorder, EventSvc: events}).dispatch()(context.Background(), payload, "entry", consumer, logrus.New())
	require.ErrorContains(t, err, "admission persistence unavailable")
	consumer.AssertExpectations(t)
}

func TestEnrollmentScheduling(t *testing.T) {
	for _, kind := range []string{domain.EnrollmentRequestKind, domain.EnrollmentApprovalPolicyKind, domain.DeviceKind, domain.CertificateSigningRequestKind} {
		for _, reason := range []domain.EventReason{domain.EventReasonResourceCreated, domain.EventReasonResourceUpdated, domain.EventReasonResourceDeleted, domain.EventReasonEnrollmentRequestApproved} {
			t.Run("When receiving "+kind+" "+string(reason)+" it should schedule only relevant changes", func(t *testing.T) {
				expected := (kind == domain.EnrollmentRequestKind && (reason == domain.EventReasonResourceCreated || reason == domain.EventReasonResourceUpdated)) || (kind == domain.EnrollmentApprovalPolicyKind && reason != domain.EventReasonEnrollmentRequestApproved)
				require.Equal(t, expected, shouldReevaluateEnrollment(domain.Event{InvolvedObject: domain.ObjectReference{Kind: kind}, Reason: reason}))
			})
		}
	}
}
