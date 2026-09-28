package agentserver

import (
	"context"
	"net/http"
	"testing"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

func TestEnrollmentHooksEvent_Snapshot(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	orgID := uuid.New()
	deviceName := "device-fingerprint-abcdefgh"
	labels := map[string]string{"env": "prod"}

	testCases := []struct {
		name        string
		device      *domain.Device
		statusCode  int32
		wantErr     codes.Code
		assertEvent func(t *testing.T, event *pb.EnrollmentHooksEvent)
	}{
		{
			name: "When condition is absent it should return condition_absent",
			device: &domain.Device{
				Metadata: domain.ObjectMeta{Name: &deviceName, Labels: &labels},
				Status:   &domain.DeviceStatus{},
			},
			statusCode: http.StatusOK,
			assertEvent: func(t *testing.T, event *pb.EnrollmentHooksEvent) {
				require.True(t, event.GetConditionAbsent())
				assert.Equal(t, int32(-1), event.GetConditionIndex())
				assert.Equal(t, "prod", event.GetLabels()["env"])
			},
		},
		{
			name: "When condition is already Pending it should map snapshot fields",
			device: &domain.Device{
				Metadata: domain.ObjectMeta{Name: &deviceName, Labels: &labels},
				Status: &domain.DeviceStatus{
					Conditions: []domain.Condition{
						{Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusFalse},
						{
							Type:    domain.ConditionTypeDeviceEnrollmentHooks,
							Status:  domain.ConditionStatusFalse,
							Reason:  domain.EnrollmentHooksReasonPending,
							Message: "notify done",
						},
					},
					EnrollmentHooks: &domain.DeviceEnrollmentHooksStatus{
						Snapshot: &domain.EnrollmentHookSnapshot{
							FailurePolicy: domain.FailurePolicyContinue,
						},
					},
				},
			},
			statusCode: http.StatusOK,
			assertEvent: func(t *testing.T, event *pb.EnrollmentHooksEvent) {
				require.False(t, event.GetConditionAbsent())
				assert.Equal(t, domain.EnrollmentHooksReasonPending, event.GetReason())
				assert.Equal(t, string(domain.ConditionStatusFalse), event.GetStatus())
				assert.Equal(t, "notify done", event.GetMessage())
				assert.Equal(t, int32(1), event.GetConditionIndex())
				assert.Equal(t, string(domain.FailurePolicyContinue), event.GetFailurePolicy())
				assert.Equal(t, "prod", event.GetLabels()["env"])
			},
		},
		{
			name:       "When device is missing it should return NotFound",
			device:     nil,
			statusCode: http.StatusNotFound,
			wantErr:    codes.NotFound,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockDevice := deviceservice.NewMockService(ctrl)
			mockDevice.EXPECT().
				GetDevice(gomock.Any(), orgID, deviceName).
				Return(tc.device, domain.Status{Code: tc.statusCode, Message: http.StatusText(int(tc.statusCode))})

			server := &AgentGrpcServer{
				log:       logrus.New(),
				deviceSvc: mockDevice,
			}
			event, err := server.enrollmentHooksEvent(context.Background(), orgID, deviceName)
			if tc.wantErr != codes.OK {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok)
				assert.Equal(t, tc.wantErr, st.Code())
				return
			}
			require.NoError(t, err)
			tc.assertEvent(t, event)
		})
	}
}

func TestWatchEnrollmentHooks_RequiresDeviceName(t *testing.T) {
	server := &AgentGrpcServer{log: logrus.New()}
	stream := &fakeWatchEnrollmentHooksStream{ctx: context.Background()}

	err := server.WatchEnrollmentHooks(&pb.WatchEnrollmentHooksRequest{}, stream)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Empty(t, stream.sent)
}

func TestWatchEnrollmentHooks_RequiresPeer(t *testing.T) {
	server := &AgentGrpcServer{log: logrus.New()}
	stream := &fakeWatchEnrollmentHooksStream{ctx: context.Background()}

	err := server.WatchEnrollmentHooks(&pb.WatchEnrollmentHooksRequest{
		DeviceName: "device-fingerprint-abcdefgh",
	}, stream)
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
}

// fakeWatchEnrollmentHooksStream is a minimal server stream for unit tests.
type fakeWatchEnrollmentHooksStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*pb.EnrollmentHooksEvent
}

func (f *fakeWatchEnrollmentHooksStream) Context() context.Context { return f.ctx }

func (f *fakeWatchEnrollmentHooksStream) Send(event *pb.EnrollmentHooksEvent) error {
	// Keep the pointer; do not value-copy the protobuf (embeds sync.Mutex).
	f.sent = append(f.sent, event)
	return nil
}

func (f *fakeWatchEnrollmentHooksStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeWatchEnrollmentHooksStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeWatchEnrollmentHooksStream) SetTrailer(metadata.MD)       {}
