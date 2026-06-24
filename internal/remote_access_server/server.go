package remote_access_server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	pb "github.com/flightctl/flightctl/api/grpc/v1"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	grpcAuth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// Server provides the flightctl-remote-access service: a WebSocket HTTP server
// and a gRPC RouterService that bridges agent streams to active AppConsoleSessions.
type Server struct {
	pb.UnimplementedRouterServiceServer
	log             logrus.FieldLogger
	cfg             *config.Config
	dataStore       store.Store
	grpcServer      *grpc.Server
	httpListener    net.Listener
	agentListener   net.Listener
	serverTLSConfig *tls.Config
	agentTLSConfig  *tls.Config
	pendingStreams  *sync.Map
	// httpHandler serves port 3444: user-facing WebSocket console (API-like middleware).
	httpHandler http.Handler
	// identityMapper maps authenticated identities to DB organisations; must be Start()ed in Run().
	identityMapper *service.IdentityMapper
}

// storeAppConsoleService adapts store.Device to console.AppConsoleDeviceService.
type storeAppConsoleService struct {
	deviceStore store.Device
}

func (s *storeAppConsoleService) GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
	result, err := s.deviceStore.Get(ctx, orgId, name)
	return result, service.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (s *storeAppConsoleService) UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, error) {
	return s.deviceStore.Update(ctx, orgId, &device, fieldsToUnset, false, nil, nil)
}

// New creates a Server with an HTTP listener at cfg.RemoteAccessService.Address (TLS terminated
// at the service when cfg.RemoteAccessService.DisableTLS is false, or plain HTTP when true for
// deployments where TLS is handled upstream) and an mTLS gRPC+HTTP mux listener at
// cfg.RemoteAccessService.AgentEndpointAddress. The db store, KV-backed rendered.Publisher, and
// auth config are needed for annotation management and auth enforcement.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	ca *crypto.CAClient,
	serverCerts *crypto.TLSCertificateConfig,
	dataStore store.Store,
	publisher console.RenderedVersionPublisher,
	multiAuth *authn.MultiAuth,
) (*Server, error) {
	if cfg.RemoteAccessService == nil {
		return nil, fmt.Errorf("remoteAccessService config section is required")
	}

	serverTLSConfig, agentTLSConfig, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	if err != nil {
		return nil, fmt.Errorf("building agent TLS config: %w", err)
	}

	// Plain TCP listener; ServeTLS is called in Run() when DisableTLS is false
	// so the service terminates TLS itself (e.g. OCP/Helm passthrough route).
	httpListener, err := net.Listen("tcp", cfg.RemoteAccessService.Address)
	if err != nil {
		return nil, fmt.Errorf("listening on service address %q: %w", cfg.RemoteAccessService.Address, err)
	}

	// Plain TCP listener — ServeTLS is called in Run() so that Go's net/http
	// stack configures HTTP/2 (ALPN "h2") automatically, which gRPC requires.
	agentListener, err := net.Listen("tcp", cfg.RemoteAccessService.AgentEndpointAddress)
	if err != nil {
		_ = httpListener.Close()
		return nil, fmt.Errorf("listening on agent endpoint address %q: %w", cfg.RemoteAccessService.AgentEndpointAddress, err)
	}

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainStreamInterceptor(grpcAuth.StreamServerInterceptor(fcmiddleware.GrpcAuthMiddleware)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 15 * time.Minute,
			Time:              2 * time.Minute,
			Timeout:           20 * time.Second,
		}),
	)

	// Identity mapper: created here, started in Run() to manage its lifecycle with the context.
	orgProvisioner := service.NewOrgProvisioner(dataStore, log)
	identityMapper := service.NewIdentityMapper(dataStore, orgProvisioner, log)
	identityMappingMiddleware := fcmiddleware.NewIdentityMappingMiddleware(identityMapper, log)

	s := &Server{
		log:             log,
		cfg:             cfg,
		dataStore:       dataStore,
		grpcServer:      grpcServer,
		httpListener:    httpListener,
		agentListener:   agentListener,
		serverTLSConfig: serverTLSConfig,
		agentTLSConfig:  agentTLSConfig,
		pendingStreams:  &sync.Map{},
		identityMapper:  identityMapper,
	}
	pb.RegisterRouterServiceServer(grpcServer, s)

	svc := &storeAppConsoleService{deviceStore: dataStore.Device()}
	appConsoleMgr := console.NewAppConsoleSessionManager(svc, log, s, publisher)
	appConsoleHandler := NewAppConsoleHandler(log, appConsoleMgr)

	authZ, err := auth.InitMultiAuthZ(cfg, log)
	if err != nil {
		_ = agentListener.Close()
		_ = httpListener.Close()
		return nil, fmt.Errorf("initializing authorization: %w", err)
	}

	// Port 3444: user-facing WebSocket console.
	// Middleware mirrors flightctl-api: AuthN → IdentityMapping → OrgExtraction → AuthZ.
	r := chi.NewRouter()

	// Health check endpoints bypass auth and rate limiting.
	r.Group(func(r chi.Router) {
		hc := cfg.RemoteAccessService.HealthChecks
		if hc != nil && hc.Enabled {
			r.Method(http.MethodGet, hc.LivenessPath, apiserver.HealthzHandler())
			r.Method(http.MethodGet, hc.ReadinessPath,
				apiserver.ReadyzHandler(time.Duration(hc.ReadinessTimeout), dataStore))
		}
	})

	r.Group(func(r chi.Router) {
		if multiAuth != nil {
			r.Use(auth.CreateAuthNMiddleware(multiAuth, log))
		}
		r.Use(identityMappingMiddleware.MapIdentityToDB)
		r.Use(fcmiddleware.ExtractAndValidateOrg(fcmiddleware.QueryOrgIDExtractor, log))
		r.Use(auth.CreateAuthZMiddleware(authZ, log))
		apiserver.ConfigureRateLimiterFromConfig(r, cfg.RemoteAccessService.RateLimit, apiserver.RateLimitScopeGeneral)
		appConsoleHandler.RegisterRoutes(r)
	})

	s.httpHandler = otelhttp.NewHandler(r, "remote-access-http-server")

	return s, nil
}

// Run starts both listeners concurrently and blocks until ctx is cancelled or a
// listener exits unexpectedly.
func (s *Server) Run(ctx context.Context) error {
	s.identityMapper.Start()
	defer s.identityMapper.Stop()

	svcCfg := s.cfg.RemoteAccessService
	// Port 7444: agent gRPC endpoint.
	// HTTP fallback returns 404 — agents connect via gRPC only; gRPC auth is
	// handled by the GrpcAuthMiddleware interceptor on the grpcServer.
	agentSrv := &http.Server{
		Addr:              svcCfg.AgentEndpointAddress,
		Handler:           grpcMuxHandlerFunc(s.grpcServer, s.httpHandler, s.log),
		ReadTimeout:       time.Duration(svcCfg.HttpReadTimeout),
		ReadHeaderTimeout: time.Duration(svcCfg.HttpReadHeaderTimeout),
		WriteTimeout:      time.Duration(svcCfg.HttpWriteTimeout),
		IdleTimeout:       time.Duration(svcCfg.HttpIdleTimeout),
		MaxHeaderBytes:    svcCfg.HttpMaxHeaderBytes,
		ConnContext:       fcmiddleware.TLSClientCertConnContext(s.log),
	}

	httpSrv := &http.Server{
		Addr:              svcCfg.Address,
		Handler:           s.httpHandler,
		ReadTimeout:       time.Duration(svcCfg.HttpReadTimeout),
		ReadHeaderTimeout: time.Duration(svcCfg.HttpReadHeaderTimeout),
		WriteTimeout:      time.Duration(svcCfg.HttpWriteTimeout),
		IdleTimeout:       time.Duration(svcCfg.HttpIdleTimeout),
		MaxHeaderBytes:    svcCfg.HttpMaxHeaderBytes,
	}

	serveErrCh := make(chan error, 2)

	go func() {
		s.log.Printf("Remote-access agent listener on %s", s.agentListener.Addr())
		agentSrv.TLSConfig = s.agentTLSConfig
		if err := agentSrv.ServeTLS(s.agentListener, "", ""); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
		}
	}()

	go func() {
		s.log.Printf("Remote-access HTTP listener on %s (disableTLS=%v)", s.httpListener.Addr(), s.cfg.RemoteAccessService.DisableTLS)
		var err error
		if s.cfg.RemoteAccessService.DisableTLS {
			err = httpSrv.Serve(s.httpListener)
		} else {
			httpSrv.TLSConfig = s.serverTLSConfig
			err = httpSrv.ServeTLS(s.httpListener, "", "")
		}
		if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		s.log.Println("Shutdown signal received:", ctx.Err())
	case runErr = <-serveErrCh:
		s.log.Errorf("listener exited unexpectedly: %v", runErr)
	}

	ctxTimeout, cancel := context.WithTimeout(context.Background(), apiserver.GracefulShutdownTimeout)
	defer cancel()
	_ = agentSrv.Shutdown(ctxTimeout)
	_ = httpSrv.Shutdown(ctxTimeout)

	grpcStopCh := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(grpcStopCh)
	}()
	select {
	case <-grpcStopCh:
	case <-ctxTimeout.Done():
		s.grpcServer.Stop()
	}

	return runErr
}

// grpcMuxHandlerFunc routes incoming requests to grpcServer (gRPC) or
// httpHandler (HTTP) based on the Content-Type header.
func grpcMuxHandlerFunc(grpcServer *grpc.Server, httpHandler http.Handler, log logrus.FieldLogger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
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
