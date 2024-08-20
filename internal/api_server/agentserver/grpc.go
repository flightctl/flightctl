package agentserver

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	grpcAuth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const SessionIDKey = "session-id"
const ClientNameKey = "client-name"

type AgentGrpcServer struct {
	pb.UnimplementedRouterServiceServer
	log            logrus.FieldLogger
	cfg            *config.Config
	tlsConfig      *tls.Config
	pendingStreams *sync.Map
}

// New returns a new instance of a flightctl server.
func NewAgentGrpcServer(
	log logrus.FieldLogger,
	cfg *config.Config,
	tlsConfig *tls.Config,
) *AgentGrpcServer {
	return &AgentGrpcServer{
		log:            log,
		cfg:            cfg,
		tlsConfig:      tlsConfig,
		pendingStreams: &sync.Map{},
	}
}

func (s *AgentGrpcServer) Run(ctx context.Context) error {
	s.log.Printf("Initializing Agent-side gRPC server: %s", s.cfg.Service.AgentGrpcAddress)
	tlsCredentials := credentials.NewTLS(s.tlsConfig)
	server := grpc.NewServer(
		grpc.Creds(tlsCredentials),
		grpc.ChainStreamInterceptor(grpcAuth.StreamServerInterceptor(middleware.GrpcAuthMiddleware)),
	)
	pb.RegisterRouterServiceServer(server, s)

	listener, err := net.Listen("tcp", s.cfg.Service.AgentGrpcAddress)

	if err != nil {
		s.log.Fatalf("cannot start server: %s", err)
	}

	go func() {
		<-ctx.Done()
		server.Stop()
	}()

	return server.Serve(listener)
}

type streamCtx struct {
	cancel context.CancelFunc
	stream pb.RouterService_StreamServer
	closed bool // one side closed the connection and we should not accept any more messages
}

func (s *AgentGrpcServer) Stream(stream pb.RouterService_StreamServer) error {
	ctx := stream.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.InvalidArgument, "missing metadata")
	}

	sessionIds := md.Get(SessionIDKey)
	if len(sessionIds) != 1 {
		return status.Error(codes.InvalidArgument, "missing "+SessionIDKey)
	}
	sessionId := sessionIds[0]

	// this should eventually come from the TLS context
	clientNames := md.Get(ClientNameKey)
	if len(clientNames) != 1 {
		return status.Error(codes.InvalidArgument, "missing "+ClientNameKey)
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
		if actual.(streamCtx).closed || otherSideStream == nil {
			// the other side closed the connection, we should not accept any more messages
			s.log.Infof("client %s, attempted connection to %s which was already closed", clientName, sessionId)
			return nil
		}
		err := forward(ctx, stream, otherSideStream)
		if errors.Is(err, io.EOF) {
			// one side closed the connection, we should not accept any more messages
			s.log.Infof("one client disconnected from session %s, closing", sessionId)

			// we try to send a close message to both sides, no error checking, best effort
			err := stream.Send(&pb.StreamResponse{Closed: true})
			if err != nil {
				s.log.Warningf("sending close message to stream %s: %s", sessionId, err)
			}

			err = otherSideStream.Send(&pb.StreamResponse{Closed: true})
			if err != nil {
				s.log.Warningf("sending close message to stream %s: %s", sessionId, err)
			}

			// free the other stream reference, and mark this one as closed
			stctx := actual.(streamCtx)
			stctx.stream = nil
			stctx.closed = true
			s.pendingStreams.Store(sessionId, stctx)
			return nil
		} else {
			actual.(streamCtx).cancel()
			return err
		}
	} else {
		// the map did not have a value, we are the first client, so we wait for the second
		s.log.Infof("client %s waiting for peer %s", clientName, sessionId)
		<-ctx.Done()
		return nil
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
