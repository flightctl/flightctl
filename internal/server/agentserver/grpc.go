package agentserver

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sync"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

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
		pendingStreams: &sync.Map{},
	}
}

func (s *AgentGrpcServer) Run(ctx context.Context) error {
	s.log.Println("Initializing Agent-side gRPC server")
	tlsCredentials := credentials.NewTLS(s.tlsConfig)
	server := grpc.NewServer(grpc.Creds(tlsCredentials))
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
}

func (s *AgentGrpcServer) Stream(stream pb.RouterService_StreamServer) error {
	ctx := stream.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.InvalidArgument, "missing metadata")
	}

	sessionIds := md.Get("session-id")
	if len(sessionIds) != 1 {
		return status.Error(codes.InvalidArgument, "missing session-id")
	}
	sessionId := sessionIds[0]

	// this should eventually come from the TLS context
	clientNames := md.Get("client-name")
	if len(clientNames) != 1 {
		return status.Error(codes.InvalidArgument, "missing client-name")
	}
	clientName := clientNames[0]

	ctx, cancel := context.WithCancel(ctx)

	sctx := streamCtx{
		cancel: cancel,
		stream: stream,
	}

	actual, loaded := s.pendingStreams.LoadOrStore(sessionId, sctx)
	// if the map already had a value, we are the second client, so we can start the forwarding
	// between both clients
	if loaded {
		s.log.Infof("client %s connected to session %s\n", clientName, sessionId)
		defer actual.(streamCtx).cancel()
		return forward(ctx, stream, actual.(streamCtx).stream)
	} else {
		// the map did not have a value, we are the first client, so we wait for the second
		s.log.Infof("client %s waiting for peer %s\n", clientName, sessionId)
		<-ctx.Done()
		return nil
	}
}

func pipe(a pb.RouterService_StreamServer, b pb.RouterService_StreamServer) error {
	for {
		msg, err := a.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		err = b.Send(&pb.StreamResponse{
			Payload: msg.GetPayload(),
		})
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func forward(ctx context.Context, a pb.RouterService_StreamServer, b pb.RouterService_StreamServer) error {
	g, _ := errgroup.WithContext(ctx)
	g.Go(func() error { return pipe(a, b) })
	g.Go(func() error { return pipe(b, a) })
	return g.Wait()
}
