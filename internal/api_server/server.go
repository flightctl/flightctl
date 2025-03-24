package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/sirupsen/logrus"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
)

type Server struct {
	log                logrus.FieldLogger
	cfg                *config.Config
	store              store.Store
	ca                 *crypto.CA
	listener           net.Listener
	queuesProvider     queues.Provider
	metrics            *instrumentation.ApiMetrics
	consoleEndpointReg console.InternalSessionRegistration
}

// New returns a new instance of a flightctl server.
func New(
	log logrus.FieldLogger,
	cfg *config.Config,
	store store.Store,
	ca *crypto.CA,
	listener net.Listener,
	queuesProvider queues.Provider,
	metrics *instrumentation.ApiMetrics,
	consoleEndpointReg console.InternalSessionRegistration,
) *Server {
	return &Server{
		log:                log,
		cfg:                cfg,
		store:              store,
		ca:                 ca,
		listener:           listener,
		queuesProvider:     queuesProvider,
		metrics:            metrics,
		consoleEndpointReg: consoleEndpointReg,
	}
}

func oapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

// If we got back multiple errors of the format:
// Error at "/path/to/invalid/input": ...
// then we don't want to return all of them because it will be too
// much information for the user. We assume that the error for the
// longest path will contain the most relevant error, so return only
// that one.
var pathRegex = regexp.MustCompile(`Error at \"/(.*)\":`)

func oapiMultiErrorHandler(errs openapi3.MultiError) (int, error) {
	if len(errs) == 0 {
		return http.StatusInternalServerError, nil
	}

	// Regex to extract the path inside quotes after "Error at "
	allMatchFormat := true
	var longestPathError error
	var longestPathErrorIndex int
	shortErrorMessages := make([]string, 0, len(errs))
	longestPathLength := -1

	for i, e := range errs {
		errMsg := e.Error()
		shortErr := strings.SplitN(errMsg, "\n", 2)[0] // Take everything until the first newline
		shortErrorMessages = append(shortErrorMessages, strings.TrimSpace(shortErr))

		matches := pathRegex.FindStringSubmatch(errMsg)
		if len(matches) < 2 {
			allMatchFormat = false
			break
		}

		// Extract the path and count the number of slashes
		path := matches[1]
		slashCount := strings.Count(path, "/")
		if slashCount > longestPathLength {
			longestPathError = e
			longestPathErrorIndex = i
			longestPathLength = slashCount
		}
	}

	if allMatchFormat && longestPathErrorIndex >= 0 {
		shortErrorMessages = append(shortErrorMessages[:longestPathErrorIndex], shortErrorMessages[longestPathErrorIndex+1:]...)
		response := fmt.Errorf("%d API errors found. The most relevant is likely:\n%s\nOther errors found were:\n%s",
			len(errs),
			longestPathError.Error(),
			strings.Join(shortErrorMessages, "\n"))
		return http.StatusBadRequest, response
	}

	// Default to returning the original errors
	return http.StatusBadRequest, errs
}

func (s *Server) Run(ctx context.Context) error {
	s.log.Println("Initializing async jobs")
	publisher, err := tasks_client.TaskQueuePublisher(s.queuesProvider)
	if err != nil {
		return err
	}
	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	callbackManager := tasks_client.NewCallbackManager(publisher, s.log)

	s.log.Println("Initializing API server")
	swagger, err := api.GetSwagger()
	if err != nil {
		return fmt.Errorf("failed loading swagger spec: %w", err)
	}
	// Skip server name validation
	swagger.Servers = nil

	oapiOpts := oapimiddleware.Options{
		ErrorHandler:      oapiErrorHandler,
		MultiErrorHandler: oapiMultiErrorHandler,
	}

	authMiddleware, err := auth.CreateAuthMiddleware(s.cfg, s.log)
	if err != nil {
		return fmt.Errorf("failed creating Auth Middleware: %w", err)
	}

	router := chi.NewRouter()

	// general middleware stack for all route groups
	// request size limits should come before logging to prevent DoS attacks from filling logs
	router.Use(
		middleware.RequestSize(int64(s.cfg.Service.HttpMaxRequestSize)),
		fcmiddleware.RequestSizeLimiter(s.cfg.Service.HttpMaxUrlLength, s.cfg.Service.HttpMaxNumHeaders),
		fcmiddleware.RequestID,
		middleware.Logger,
		middleware.Recoverer,
		authMiddleware,
		auth.CreatePermissionsMiddleware(s.log),
	)

	serviceHandler := service.NewServiceHandler(s.store, callbackManager, kvStore, s.ca, s.log, s.cfg.Service.BaseAgentEndpointUrl, s.cfg.Service.BaseUIUrl)

	// a group is a new mux copy, with its own copy of the middleware stack
	// this one handles the OpenAPI handling of the service
	router.Group(func(r chi.Router) {
		//NOTE(majopela): keeping metrics middleware separate from the rest of the middleware stack
		// to avoid issues with websocket connections
		if s.metrics != nil {
			r.Use(s.metrics.ApiServerMiddleware)
		}
		r.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts))

		h := transport.NewTransportHandler(serviceHandler)
		server.HandlerFromMux(h, r)
	})

	consoleSessionManager := console.NewConsoleSessionManager(serviceHandler, s.log, s.consoleEndpointReg)
	ws := transport.NewWebsocketHandler(s.ca, s.log, consoleSessionManager)
	ws.RegisterRoutes(router)

	srv := fcmiddleware.NewHTTPServer(router, s.log, s.cfg.Service.Address, s.cfg)

	go func() {
		<-ctx.Done()
		s.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
		kvStore.Close()
		s.queuesProvider.Stop()
		s.queuesProvider.Wait()
	}()

	s.log.Printf("Listening on %s...", s.listener.Addr().String())
	if err := srv.Serve(s.listener); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}

	return nil
}
