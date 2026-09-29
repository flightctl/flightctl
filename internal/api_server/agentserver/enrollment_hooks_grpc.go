package agentserver

import (
	"context"
	"crypto/x509"
	"net/http"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// WatchEnrollmentHooks streams EnrollmentHooks condition snapshots until the
// reason leaves NotifyPending (or the condition is absent).
func (s *AgentGrpcServer) WatchEnrollmentHooks(
	req *pb.WatchEnrollmentHooksRequest,
	stream pb.Enrollment_WatchEnrollmentHooksServer,
) error {
	ctx := stream.Context()

	if req == nil || req.GetDeviceName() == "" {
		return status.Error(codes.InvalidArgument, "device_name is required")
	}

	fingerprint, orgID, err := s.identityFromPeer(ctx)
	if err != nil {
		return err
	}
	if fingerprint != req.GetDeviceName() {
		s.log.Errorf("attempt to watch enrollment hooks for device %q with certificate fingerprint %q", req.GetDeviceName(), fingerprint)
		return status.Error(codes.PermissionDenied, "certificate does not match requested device")
	}

	// Subscribe before the snapshot read so a transition between the DB read
	// and the wait cannot be dropped.
	notifyCh, unsub := rendered.Bus.Instance().SubscribeEnrollmentHooks(orgID, fingerprint)
	defer unsub()

	event, err := s.enrollmentHooksEvent(ctx, orgID, fingerprint)
	if err != nil {
		return err
	}
	if err := stream.Send(event); err != nil {
		return status.Errorf(codes.Internal, "failed to send enrollment hooks snapshot: %v", err)
	}
	if enrollmentHooksWatchDone(event) {
		return nil
	}

	for {
		// Timeout and notification both fall through to a DB re-read so a
		// missed wake still converges on current condition state.
		_, waitErr := rendered.Bus.Instance().WaitOnEnrollmentHooksChannel(ctx, notifyCh)
		if waitErr != nil {
			if ctx.Err() != nil {
				return status.Error(codes.Canceled, "watch cancelled")
			}
			return status.Errorf(codes.Internal, "waiting for enrollment hooks notification: %v", waitErr)
		}

		next, err := s.enrollmentHooksEvent(ctx, orgID, fingerprint)
		if err != nil {
			return err
		}
		if next.GetReason() == event.GetReason() &&
			next.GetStatus() == event.GetStatus() &&
			next.GetMessage() == event.GetMessage() &&
			next.GetConditionAbsent() == event.GetConditionAbsent() {
			if enrollmentHooksWatchDone(next) {
				return nil
			}
			continue
		}

		event = next
		if err := stream.Send(event); err != nil {
			return status.Errorf(codes.Internal, "failed to send enrollment hooks event: %v", err)
		}
		if enrollmentHooksWatchDone(event) {
			return nil
		}
	}
}

func enrollmentHooksWatchDone(event *pb.EnrollmentHooksEvent) bool {
	if event == nil || event.GetConditionAbsent() {
		return true
	}
	return event.GetReason() != domain.EnrollmentHooksReasonNotifyPending
}

func (s *AgentGrpcServer) enrollmentHooksEvent(ctx context.Context, orgID uuid.UUID, name string) (*pb.EnrollmentHooksEvent, error) {
	device, st := s.deviceSvc.GetDevice(ctx, orgID, name)
	if st.Code != http.StatusOK {
		if st.Code == http.StatusNotFound {
			return nil, status.Errorf(codes.NotFound, "device %q not found", name)
		}
		return nil, status.Errorf(codes.Internal, "failed to get device: %s", st.Message)
	}

	event := &pb.EnrollmentHooksEvent{
		ConditionIndex:  -1,
		ConditionAbsent: true,
	}
	if device.Metadata.Labels != nil {
		event.Labels = *device.Metadata.Labels
	}
	if device.Status != nil &&
		device.Status.EnrollmentHooks != nil &&
		device.Status.EnrollmentHooks.Snapshot != nil {
		event.FailurePolicy = string(device.Status.EnrollmentHooks.Snapshot.FailurePolicy)
	}

	if device.Status == nil {
		return event, nil
	}

	for i := range device.Status.Conditions {
		cond := &device.Status.Conditions[i]
		if cond.Type != domain.ConditionTypeDeviceEnrollmentHooks {
			continue
		}
		event.ConditionAbsent = false
		event.Reason = cond.Reason
		event.Status = string(cond.Status)
		event.Message = cond.Message
		event.ConditionIndex = int32(i) //nolint:gosec // condition index fits int32
		return event, nil
	}
	return event, nil
}

func (s *AgentGrpcServer) identityFromPeer(ctx context.Context) (string, uuid.UUID, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", uuid.Nil, status.Error(codes.Unauthenticated, "no peer found")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return "", uuid.Nil, status.Error(codes.Unauthenticated, "failed to verify client certificate")
	}
	cert := tlsInfo.State.VerifiedChains[0][0]
	return s.identityFromCert(cert)
}

func (s *AgentGrpcServer) identityFromCert(cert *x509.Certificate) (string, uuid.UUID, error) {
	if s.ca == nil || s.ca.Cfg == nil {
		return "", uuid.Nil, status.Error(codes.Internal, "CA not configured")
	}
	fingerprint, err := signer.DeviceFingerprintFromCN(s.ca.Cfg, cert.Subject.CommonName)
	if err != nil {
		return "", uuid.Nil, status.Errorf(codes.Unauthenticated, "invalid client certificate: %v", err)
	}
	orgID, present, err := signer.GetOrgIDExtensionFromCert(cert)
	if err != nil {
		return "", uuid.Nil, status.Errorf(codes.Unauthenticated, "failed to extract organization ID: %v", err)
	}
	if !present {
		orgID = org.DefaultID
	}
	return fingerprint, orgID, nil
}
