package remote_access_server

import (
	"context"
	"crypto/x509"
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
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	authproviderservice "github.com/flightctl/flightctl/internal/service/authprovider"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	authproviderstore "github.com/flightctl/flightctl/internal/store/authprovider"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	grpcAuth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"gorm.io/gorm"
)

// Server provides the flightctl-remote-access service: a WebSocket HTTP server
// and a gRPC RouterService that bridges agent streams to active AppConsoleSessions.
type Server struct {
	pb.UnimplementedRouterServiceServer
	log            logrus.FieldLogger
	cfg            *config.Config
	caBundleCerts  []*x509.Certificate
	serverCerts    *crypto.TLSCertificateConfig
	db             *gorm.DB
	notifier       console.ConsoleEventNotifier
	pendingStreams *sync.Map
}

// storeAppConsoleService adapts devicestore.Store to console.AppConsoleDeviceService.
type storeAppConsoleService struct {
	deviceStore devicestore.Store
}

func (s *storeAppConsoleService) GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
	result, err := s.deviceStore.Get(ctx, orgId, name)
	return result, service.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (s *storeAppConsoleService) UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, error) {
	return s.deviceStore.Update(ctx, orgId, &device, fieldsToUnset, nil, nil)
}

// New returns a new Server. All initialisation requiring a context (listeners,
// auth, router) is deferred to Run(), matching the imagebuilder-api pattern.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	caBundleCerts []*x509.Certificate,
	serverCerts *crypto.TLSCertificateConfig,
	db *gorm.DB,
	notifier console.ConsoleEventNotifier,
) (*Server, error) {
	if cfg.RemoteAccessService == nil {
		return nil, fmt.Errorf("remoteAccessService config section is required")
	}
	return &Server{
		log:            log,
		cfg:            cfg,
		caBundleCerts:  caBundleCerts,
		serverCerts:    serverCerts,
		db:             db,
		notifier:       notifier,
		pendingStreams: &sync.Map{},
	}, nil
}

// Run initialises all runtime components (listeners, auth, router, gRPC) and
// blocks until ctx is cancelled or a listener exits unexpectedly.
func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing remote-access server")

	serverTLSConfig, agentTLSConfig, err := crypto.TLSConfigForServer(s.caBundleCerts, s.serverCerts)
	if err != nil {
		return fmt.Errorf("building TLS config: %w", err)
	}

	// Bind both listeners early so port conflicts surface before auth/DB work.
	httpListener, err := net.Listen("tcp", s.cfg.RemoteAccessService.Address)
	if err != nil {
		return fmt.Errorf("listening on service address %q: %w", s.cfg.RemoteAccessService.Address, err)
	}
	defer httpListener.Close()

	agentListener, err := net.Listen("tcp", s.cfg.RemoteAccessService.AgentEndpointAddress)
	if err != nil {
		return fmt.Errorf("listening on agent endpoint address %q: %w", s.cfg.RemoteAccessService.AgentEndpointAddress, err)
	}
	defer agentListener.Close()

	authProviderStore := authproviderstore.NewAuthProviderStore(s.db, s.log.WithField("pkg", "authprovider-store"))
	catalogStore := catalogstore.NewCatalogStore(s.db, s.log.WithField("pkg", "catalog-store"))
	organizationStore := organizationstore.NewOrganizationStore(s.db)
	deviceStore := devicestore.NewDeviceStore(s.db, s.log.WithField("pkg", "device-store"))
	eventStore := eventstore.NewEventStore(s.db, s.log.WithField("pkg", "event-store"))
	eventsSvc := events.NewServiceHandler(eventStore, nil, s.log)

	// Auth — matches imagebuilder-api: tracing-wrapped store-backed service,
	// InitMultiAuth, then Start(ctx) in a goroutine with an error channel.
	authProviderSvc := authproviderservice.WrapWithTracing(authproviderservice.NewServiceHandler(authProviderStore, eventsSvc, s.log))
	authN, err := auth.InitMultiAuth(s.cfg, s.log, authProviderSvc)
	if err != nil {
		return fmt.Errorf("initializing authentication: %w", err)
	}

	authZ, err := auth.InitMultiAuthZ(s.cfg, s.log)
	if err != nil {
		return fmt.Errorf("initializing authorization: %w", err)
	}
	if multiAuthZ, ok := authZ.(*auth.MultiAuthZ); ok {
		multiAuthZ.Start(ctx)
		s.log.Debug("Started MultiAuthZ with context-based cache lifecycle")
	}

	authErrCh := make(chan error, 1)
	go func() {
		err := authN.Start(ctx)
		if err == nil && ctx.Err() == nil {
			err = fmt.Errorf("auth provider loader stopped unexpectedly")
		}
		if err != nil && ctx.Err() == nil {
			select {
			case authErrCh <- fmt.Errorf("auth provider loader failed: %w", err):
			default:
			}
		}
	}()

	// Identity mapper.
	orgProvisioner := service.NewOrgProvisioner(catalogStore, s.log)
	identityMapper := service.NewIdentityMapper(organizationStore, orgProvisioner, s.log)
	identityMappingMiddleware := fcmiddleware.NewIdentityMappingMiddleware(identityMapper, s.log)
	identityMapper.Start()
	defer identityMapper.Stop()

	// gRPC server.
	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainStreamInterceptor(grpcAuth.StreamServerInterceptor(fcmiddleware.GrpcAuthMiddleware)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: 15 * time.Minute,
			Time:              2 * time.Minute,
			Timeout:           20 * time.Second,
		}),
	)
	pb.RegisterRouterServiceServer(grpcServer, s)

	// App console.
	svc := &storeAppConsoleService{deviceStore: deviceStore}
	appConsoleMgr := console.NewAppConsoleSessionManager(svc, s.log, s, s.notifier)
	appConsoleHandler := NewAppConsoleHandler(s.log, appConsoleMgr)

	// HTTP router — mirrors flightctl-api: AuthN → IdentityMapping → OrgExtraction → AuthZ.
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		hc := s.cfg.RemoteAccessService.HealthChecks
		if hc != nil && hc.Enabled {
			r.Method(http.MethodGet, hc.LivenessPath, apiserver.HealthzHandler())
			r.Method(http.MethodGet, hc.ReadinessPath,
				apiserver.ReadyzHandler(time.Duration(hc.ReadinessTimeout), &store.DBHealthChecker{DB: s.db}))
		}
	})
	r.Group(func(r chi.Router) {
		r.Use(auth.CreateAuthNMiddleware(authN, s.log))
		r.Use(identityMappingMiddleware.MapIdentityToDB)
		r.Use(fcmiddleware.ExtractAndValidateOrg(fcmiddleware.QueryOrgIDExtractor, s.log))
		r.Use(auth.CreateAuthZMiddleware(authZ, s.log))
		apiserver.ConfigureRateLimiterFromConfig(r, s.cfg.RemoteAccessService.RateLimit, apiserver.RateLimitScopeGeneral)
		appConsoleHandler.RegisterRoutes(r)
	})
	httpHandler := otelhttp.NewHandler(r, "remote-access-http-server")

	// HTTP servers.
	svcCfg := s.cfg.RemoteAccessService
	agentSrv := &http.Server{
		Addr:              svcCfg.AgentEndpointAddress,
		Handler:           grpcMuxHandlerFunc(grpcServer, http.NotFoundHandler(), s.log),
		ReadTimeout:       time.Duration(svcCfg.HttpReadTimeout),
		ReadHeaderTimeout: time.Duration(svcCfg.HttpReadHeaderTimeout),
		WriteTimeout:      time.Duration(svcCfg.HttpWriteTimeout),
		IdleTimeout:       time.Duration(svcCfg.HttpIdleTimeout),
		MaxHeaderBytes:    svcCfg.HttpMaxHeaderBytes,
		ConnContext:       fcmiddleware.TLSClientCertConnContext(s.log),
	}
	httpSrv := &http.Server{
		Addr:              svcCfg.Address,
		Handler:           httpHandler,
		ReadTimeout:       time.Duration(svcCfg.HttpReadTimeout),
		ReadHeaderTimeout: time.Duration(svcCfg.HttpReadHeaderTimeout),
		WriteTimeout:      time.Duration(svcCfg.HttpWriteTimeout),
		IdleTimeout:       time.Duration(svcCfg.HttpIdleTimeout),
		MaxHeaderBytes:    svcCfg.HttpMaxHeaderBytes,
	}

	serveErrCh := make(chan error, 2)
	go func() {
		s.log.Printf("Remote-access agent listener on %s", agentListener.Addr())
		agentSrv.TLSConfig = agentTLSConfig
		if err := agentSrv.ServeTLS(agentListener, "", ""); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
		}
	}()
	go func() {
		s.log.Printf("Remote-access HTTP listener on %s (disableTLS=%v)", httpListener.Addr(), s.cfg.RemoteAccessService.DisableTLS)
		var err error
		if s.cfg.RemoteAccessService.DisableTLS {
			err = httpSrv.Serve(httpListener)
		} else {
			httpSrv.TLSConfig = serverTLSConfig
			err = httpSrv.ServeTLS(httpListener, "", "")
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
	case runErr = <-authErrCh:
		s.log.Errorf("auth provider loader failed: %v", runErr)
	}

	ctxTimeout, cancel := context.WithTimeout(context.Background(), apiserver.GracefulShutdownTimeout)
	defer cancel()
	_ = agentSrv.Shutdown(ctxTimeout)
	_ = httpSrv.Shutdown(ctxTimeout)

	grpcStopCh := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(grpcStopCh)
	}()
	select {
	case <-grpcStopCh:
	case <-ctxTimeout.Done():
		grpcServer.Stop()
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
