package agentserver

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/consts"
	grpcAuth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type AgentGrpcServer struct {
	pb.UnimplementedRouterServiceServer
	log            logrus.FieldLogger
	cfg            *config.Config
	pendingStreams *sync.Map
}

// New returns a new instance of a flightctl server.
func NewAgentGrpcServer(
	log logrus.FieldLogger,
	cfg *config.Config,
) *AgentGrpcServer {
	return &AgentGrpcServer{
		log:            log,
		cfg:            cfg,
		pendingStreams: &sync.Map{},
	}
}

func (s *AgentGrpcServer) PrepareGRPCService() *grpc.Server {
	server := grpc.NewServer(grpc.ChainStreamInterceptor(grpcAuth.StreamServerInterceptor(middleware.GrpcAuthMiddleware)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 15 * time.Minute, // Close idle connections after 15 minutes
			Time:              2 * time.Minute,  // Send keepalive ping every 2 minutes
			Timeout:           20 * time.Second, // Wait 20s for client response before closing
		}))
	pb.RegisterRouterServiceServer(server, s)
	return server
}

type streamCtx struct {
	cancel  context.CancelFunc
	stream  pb.RouterService_StreamServer
	session *console.ConsoleSession // we let the console sessions coexist for a while until UI gets fully migrated out of gRPC
	closed  bool                    // one side closed the connection and we should not accept any more messages
}

// This function is called by the console session manager to signal that it is ready to accept connections
// from the agent. This is a way to provide the gRPC service with the session UUID and the channels
func (s *AgentGrpcServer) StartSession(session *console.ConsoleSession) error {
	sctx := streamCtx{
		cancel:  nil,
		stream:  nil,
		closed:  false,
		session: session,
	}
	s.log.Infof("the console manager started the session %s for device %s, waiting for stream", session.UUID, session.DeviceName)
	// we store the session in the map, so when the agent connects we can start forwarding
	// bytes between the two client and the device agent
	s.pendingStreams.Store(session.UUID, sctx)
	return nil
}

// This function is called by the console session manager to signal that the session has been closed
// from the other side
func (s *AgentGrpcServer) CloseSession(session *console.ConsoleSession) error {
	s.log.Infof("the console manager closed the session %s for device %s", session.UUID, session.DeviceName)
	s.pendingStreams.Delete(session.UUID)
	return nil
}
func (s *AgentGrpcServer) Stream(stream pb.RouterService_StreamServer) error {
	ctx := stream.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.InvalidArgument, "missing metadata")
	}

	sessionIds := md.Get(consts.GrpcSessionIDKey)
	if len(sessionIds) != 1 {
		return status.Error(codes.InvalidArgument, "missing "+consts.GrpcSessionIDKey)
	}
	sessionId := sessionIds[0]

	// this should eventually come from the TLS context
	clientNames := md.Get(consts.GrpcClientNameKey)
	if len(clientNames) != 1 {
		return status.Error(codes.InvalidArgument, "missing "+consts.GrpcClientNameKey)
	}
	clientName := clientNames[0]

	ctx, cancel := context.WithCancel(ctx)

	sctx := streamCtx{
		cancel: cancel,
		stream: stream,
		closed: false,
		// TODO: Add expiration fields so we can clean up old streams ver time
	}

	actual, loaded := s.pendingStreams.LoadOrStore(sessionId, sctx)
	// if the map already had a value, we are the second client, so we can start the forwarding
	// between both clients
	if loaded {
		s.log.Infof("client %s connected to session %s", clientName, sessionId)
		otherSideStream := actual.(streamCtx).stream
		otherSideSession := actual.(streamCtx).session
		if actual.(streamCtx).closed || otherSideStream == nil && otherSideSession == nil {
			// the other side closed the connection, we should not accept any more messages
			s.log.Infof("client %s, attempted connection to %s which was already closed", clientName, sessionId)
			return nil
		}
		// TODO(majopela): handling of the gRPC<->gRPC communication, we should remove this once the console UI is fully migrated to the new ws API
		if otherSideStream != nil {
			s.log.Infof("client %s connected to session %s, forwarding gRPC<->gRPC", clientName, sessionId)
			return s.forwardGrpcOnlyStream(ctx, stream, otherSideStream, sessionId, actual)
		}
		// There is a ws console session on the other side, we should forward the ws connection to the console session
		if otherSideSession != nil {
			s.log.Infof("client %s connected to session %s, forwarding ws console session", clientName, sessionId)

			selectedProtocols := md.Get(consts.GrpcSelectedProtocolKey)
			if len(selectedProtocols) != 1 {
				close(otherSideSession.ProtocolCh)
				return status.Error(codes.InvalidArgument, "missing "+consts.GrpcSelectedProtocolKey)
			}
			selectedProtocol := selectedProtocols[0]

			// Tell the other side to use the selected protocol
			otherSideSession.ProtocolCh <- selectedProtocol
			return s.forwardConsoleSession(ctx, stream, otherSideSession, sessionId, actual)
		}

	} else {
		// the map did not have a value, we are the first client, so we wait for the second
		s.log.Infof("client %s waiting for peer %s", clientName, sessionId)
		<-ctx.Done()
		return nil
	}
	return nil
}

func (s *AgentGrpcServer) forwardConsoleSession(ctx context.Context, stream pb.RouterService_StreamServer, otherSideSession *console.ConsoleSession, sessionId string, actual any) error {
	return s.forwardChannels(ctx, stream, otherSideSession)
}

func (s *AgentGrpcServer) forwardChannels(ctx context.Context, a pb.RouterService_StreamServer, b *console.ConsoleSession) error {
	g, _ := errgroup.WithContext(ctx)
	g.Go(func() error { return s.pipeStreamToChannel(ctx, a, b.RecvCh) })
	g.Go(func() error { return s.pipeChannelToStream(ctx, b.SendCh, a) })
	return g.Wait()
}

func (s *AgentGrpcServer) pipeStreamToChannel(ctx context.Context, a pb.RouterService_StreamServer, ch chan []byte) error {
	defer close(ch)

	for {
		select {
		case <-ctx.Done():
			s.log.Debug("context is closed")
			return io.EOF
		default:
			msg, err := a.Recv()
			if err != nil {
				s.log.Debugf("failed to receive a message: %v", err)
				return err
			}

			payload := msg.GetPayload()
			closed := msg.GetClosed()
			ch <- payload
			if closed {
				s.log.Debug("channel is closed")
				return io.EOF
			}
		}
	}
}

func (s *AgentGrpcServer) pipeChannelToStream(ctx context.Context, ch chan []byte, a pb.RouterService_StreamServer) error {
	for {
		select {
		case <-ctx.Done():
			s.log.Debug("context is closed")
			_ = a.Send(&pb.StreamResponse{Payload: []byte{}, Closed: true})
			return io.EOF

		case payload, ok := <-ch:
			if !ok {
				s.log.Debug("channel is closed")
				_ = a.Send(&pb.StreamResponse{Payload: []byte{}, Closed: true})
				return io.EOF
			}
			if err := a.Send(&pb.StreamResponse{Payload: payload}); err != nil {
				s.log.Debugf("send to agent %v", err)
				return err
			}
		}
	}
}

// TODO: this path provides support for the existing gRPC<->gRPC communication, we should remove it
// once the console UI is fully migrated to the new ws API
func (s *AgentGrpcServer) forwardGrpcOnlyStream(ctx context.Context, stream pb.RouterService_StreamServer, otherSideStream pb.RouterService_StreamServer, sessionId string, actual any) error {
	err := forward(ctx, stream, otherSideStream)
	if errors.Is(err, io.EOF) {
		// one side closed the connection, we should not accept any more messages
		// we try to send a close message to both sides, no error checking, best effort
		// free the other stream reference, and mark this one as closed
		s.log.Infof("one client disconnected from session %s, closing", sessionId)

		err := stream.Send(&pb.StreamResponse{Closed: true})
		if err != nil {
			s.log.Warningf("sending close message to stream %s: %s", sessionId, err)
		}

		err = otherSideStream.Send(&pb.StreamResponse{Closed: true})
		if err != nil {
			s.log.Warningf("sending close message to stream %s: %s", sessionId, err)
		}

		stctx := actual.(streamCtx)
		stctx.stream = nil
		stctx.closed = true
		s.pendingStreams.Store(sessionId, stctx)
		return nil
	} else {
		actual.(streamCtx).cancel()
		return err
	}
}

func pipe(a pb.RouterService_StreamServer, b pb.RouterService_StreamServer) error {
	// TODO: this is a good place to add auditing to the console
	for {
		msg, err := a.Recv()
		if err != nil {
			return err
		}

		payload := msg.GetPayload()
		closed := msg.GetClosed()
		err = b.Send(&pb.StreamResponse{
			Payload: payload,
			Closed:  closed,
		})
		if err != nil {
			return err
		}
		if closed {
			return io.EOF
		}
	}
}

func forward(ctx context.Context, a pb.RouterService_StreamServer, b pb.RouterService_StreamServer) error {
	g, _ := errgroup.WithContext(ctx)
	g.Go(func() error { return pipe(a, b) })
	g.Go(func() error { return pipe(b, a) })
	return g.Wait()
}
