package agentserver

import (
	"testing"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestEnrollmentHooksWatchDone(t *testing.T) {
	testCases := []struct {
		name  string
		event *pb.EnrollmentHooksEvent
		done  bool
	}{
		{
			name:  "When event is nil it should be done",
			event: nil,
			done:  true,
		},
		{
			name:  "When condition is absent it should be done",
			event: &pb.EnrollmentHooksEvent{ConditionAbsent: true},
			done:  true,
		},
		{
			name:  "When reason is NotifyPending it should keep watching",
			event: &pb.EnrollmentHooksEvent{Reason: domain.EnrollmentHooksReasonNotifyPending},
			done:  false,
		},
		{
			name:  "When reason is Pending it should be done",
			event: &pb.EnrollmentHooksEvent{Reason: domain.EnrollmentHooksReasonPending},
			done:  true,
		},
		{
			name:  "When reason is Failed it should be done",
			event: &pb.EnrollmentHooksEvent{Reason: domain.EnrollmentHooksReasonFailed},
			done:  true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.done, enrollmentHooksWatchDone(tc.event))
		})
	}
}
