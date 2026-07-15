package remote_access_server

import (
	"context"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/consts"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// StartSession registers an AppConsoleSession so the next gRPC Stream() call from
// the agent can rendezvous with it.
func (s *Server) StartSession(session *console.AppConsoleSession) error {
	s.log.Infof("app console session %s registered for device %s app %s", session.UUID, session.DeviceName, session.AppName)
	s.pendingStreams.Store(session.UUID, session)
	return nil
}

// CloseSession removes a previously registered AppConsoleSession.
func (s *Server) CloseSession(session *console.AppConsoleSession) error {
	s.log.Infof("app console session %s removed for device %s app %s", session.UUID, session.DeviceName, session.AppName)
	s.pendingStreams.Delete(session.UUID)
	return nil
}

// Stream implements pb.RouterServiceServer. When the agent connects it reads the
// x-session-id gRPC metadata key, looks up the matching AppConsoleSession, sends
// the selected protocol to ProtocolCh, and forwards bytes bidirectionally.
func (s *Server) Stream(stream pb.RouterService_StreamServer) error {
	ctx := stream.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.InvalidArgument, "missing metadata")
	}

	sessionIDs := md.Get(consts.GrpcSessionIDKey)
	if len(sessionIDs) != 1 {
		return status.Error(codes.InvalidArgument, "missing "+consts.GrpcSessionIDKey)
	}
	sessionID := sessionIDs[0]

	val, loaded := s.pendingStreams.LoadAndDelete(sessionID)
	if !loaded {
		s.log.Warnf("agent connected to unknown session %s", sessionID)
		return status.Error(codes.NotFound, "session not found: "+sessionID)
	}

	session, ok := val.(*console.AppConsoleSession)
	if !ok {
		return status.Error(codes.Internal, "invalid session type for "+sessionID)
	}

	selectedProtocols := md.Get(consts.GrpcSelectedProtocolKey)
	if len(selectedProtocols) != 1 {
		// The agent found a session-level failure (e.g. the app does not exist) before it
		// could select a protocol. Report it via ErrCh instead of ProtocolCh so the caller
		// can fail the client's connection before upgrading it, rather than only after.
		if agentErrs := md.Get(consts.GrpcSessionErrorKey); len(agentErrs) == 1 && agentErrs[0] != "" {
			s.log.Infof("agent reported session-level failure before protocol selection for session %s device %s app %s",
				sessionID, session.DeviceName, session.AppName)
			select {
			case session.ErrCh <- agentErrs[0]:
			default:
			}
			return status.Error(codes.FailedPrecondition, agentErrs[0])
		}
		close(session.ProtocolCh)
		return status.Error(codes.InvalidArgument, "missing "+consts.GrpcSelectedProtocolKey)
	}
	select {
	case session.ProtocolCh <- selectedProtocols[0]:
	default:
		return status.Error(codes.DeadlineExceeded, "session no longer waiting for protocol negotiation")
	}

	s.log.Infof("agent connected to app console session %s for device %s app %s, bridging streams",
		sessionID, session.DeviceName, session.AppName)
	return s.forwardChannels(ctx, stream, session)
}

func (s *Server) forwardChannels(ctx context.Context, stream pb.RouterService_StreamServer, session *console.AppConsoleSession) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go s.pipeStreamToChannel(ctx, stream, session.RecvCh, session.ErrCh)
	return s.pipeChannelToStream(ctx, session.SendCh, stream)
}

// pipeStreamToChannel forwards agent payloads from stream to ch until the stream ends,
// closing ch on every return path except the error one. When the agent reports a
// session-level error, ch is deliberately left open (never closed) instead of closed
// right after the errCh send: the consumer's select over both channels could otherwise
// race and observe the close before (or instead of) the error, silently losing it. Since
// the consumer is expected to stop reading from ch as soon as it observes errCh, leaving
// ch open here is harmless — it is simply never read from again.
func (s *Server) pipeStreamToChannel(ctx context.Context, stream pb.RouterService_StreamServer, ch chan []byte, errCh chan string) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				s.log.Debug("app console stream context closed")
			} else {
				s.log.Debugf("app console stream recv error: %v", err)
			}
			close(ch)
			return
		}
		if agentErr := msg.GetError(); agentErr != "" {
			s.log.Debugf("app console stream reported session error: %s", agentErr)
			select {
			case errCh <- agentErr:
			case <-ctx.Done():
			}
			return
		}
		select {
		case ch <- msg.GetPayload():
		case <-ctx.Done():
			s.log.Debug("app console stream context closed while forwarding payload")
			close(ch)
			return
		}
		if msg.GetClosed() {
			s.log.Debug("app console stream closed by agent")
			close(ch)
			return
		}
	}
}

func (s *Server) pipeChannelToStream(ctx context.Context, ch chan []byte, stream pb.RouterService_StreamServer) error {
	for {
		select {
		case <-ctx.Done():
			s.log.Debug("app console channel context closed")
			_ = stream.Send(&pb.StreamResponse{Payload: []byte{}, Closed: true})
			return nil
		case payload, ok := <-ch:
			if !ok {
				s.log.Debug("app console send channel closed")
				_ = stream.Send(&pb.StreamResponse{Payload: []byte{}, Closed: true})
				return nil
			}
			if err := stream.Send(&pb.StreamResponse{Payload: payload}); err != nil {
				s.log.Debugf("app console stream send error: %v", err)
				return err
			}
		}
	}
}
