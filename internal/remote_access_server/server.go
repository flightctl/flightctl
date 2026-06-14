package remote_access_server

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	grpcAuth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// Server provides the flightctl-remote-access service: an HTTP 501 stub and
// a gRPC RouterService stub that accepts and immediately closes streams.
// Future stories will replace the stubs with real console-bridging logic.
type Server struct {
	pb.UnimplementedRouterServiceServer
	log            logrus.FieldLogger
	cfg            *config.Config
	grpcServer     *grpc.Server
	httpListener   net.Listener
	agentListener  net.Listener
	agentTLSConfig *tls.Config
}

// New creates a Server with a TLS HTTP listener at cfg.Service.Address and
// an mTLS gRPC+HTTP mux listener at cfg.Service.AgentEndpointAddress.
func New(log logrus.FieldLogger, cfg *config.Config, ca *crypto.CAClient, serverCerts *crypto.TLSCertificateConfig) (*Server, error) {
	tlsConfig, agentTLSConfig, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		return nil, err
	}

	httpListener, err := middleware.NewTLSListener(cfg.Service.Address, tlsConfig)
	if err != nil {
		return nil, err
	}

	// Plain TCP listener — ServeTLS is called in Run() so that Go's net/http
	// stack configures HTTP/2 (ALPN "h2") automatically, which gRPC requires.
	agentListener, err := net.Listen("tcp", cfg.Service.AgentEndpointAddress)
	if err != nil {
		_ = httpListener.Close()
		return nil, err
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainStreamInterceptor(grpcAuth.StreamServerInterceptor(middleware.GrpcAuthMiddleware)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 15 * time.Minute,
			Time:              2 * time.Minute,
			Timeout:           20 * time.Second,
		}),
	)

	s := &Server{
		log:            log,
		cfg:            cfg,
		grpcServer:     grpcServer,
		httpListener:   httpListener,
		agentListener:  agentListener,
		agentTLSConfig: agentTLSConfig,
	}
	pb.RegisterRouterServiceServer(grpcServer, s)
	return s, nil
}

// Run starts both listeners concurrently and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	agentSrv := middleware.NewHTTPServerWithTLSContext(
		grpcMuxHandlerFunc(s.grpcServer, stubHandler(), s.log),
		s.log,
		s.cfg.Service.AgentEndpointAddress,
		s.cfg,
	)

	httpSrv := middleware.NewHTTPServer(stubHandler(), s.log, s.cfg.Service.Address, s.cfg)

	go func() {
		s.log.Printf("Remote-access agent listener on %s", s.agentListener.Addr())
		agentSrv.TLSConfig = s.agentTLSConfig
		if err := agentSrv.ServeTLS(s.agentListener, "", ""); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			s.log.Errorf("agent listener error: %v", err)
		}
	}()

	go func() {
		s.log.Printf("Remote-access HTTP stub listener on %s", s.httpListener.Addr())
		if err := httpSrv.Serve(s.httpListener); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			s.log.Errorf("HTTP stub listener error: %v", err)
		}
	}()

	<-ctx.Done()
	s.log.Println("Shutdown signal received:", ctx.Err())
	ctxTimeout, cancel := context.WithTimeout(context.Background(), apiserver.GracefulShutdownTimeout)
	defer cancel()
	_ = agentSrv.Shutdown(ctxTimeout)
	_ = httpSrv.Shutdown(ctxTimeout)
	return nil
}

// Stream implements pb.RouterServiceServer — accepts the incoming agent gRPC
// stream and immediately closes it (stub).
func (s *Server) Stream(_ pb.RouterService_StreamServer) error {
	return nil
}

// stubHandler returns HTTP 501 Not Implemented for every request.
func stubHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, http.StatusText(http.StatusNotImplemented), http.StatusNotImplemented)
	})
}

// grpcMuxHandlerFunc routes incoming requests to grpcServer (gRPC) or
// httpHandler (HTTP) based on the Content-Type header.
func grpcMuxHandlerFunc(grpcServer *grpc.Server, httpHandler http.Handler, log logrus.FieldLogger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && r.Header.Get("Content-Type") == "application/grpc" {
			type rwTimeoutSetter interface {
				SetReadDeadline(time.Time) error
				SetWriteDeadline(time.Time) error
			}
			if rtw, ok := w.(rwTimeoutSetter); ok {
				if err := rtw.SetReadDeadline(time.Time{}); err != nil {
					log.Errorf("setting gRPC read deadline: %v", err)
				}
				if err := rtw.SetWriteDeadline(time.Time{}); err != nil {
					log.Errorf("setting gRPC write deadline: %v", err)
				}
			} else {
				log.Error("cannot set gRPC deadline")
			}
			grpcServer.ServeHTTP(w, r)
		} else {
			httpHandler.ServeHTTP(w, r)
		}
	})
}
