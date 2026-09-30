package healthcheckextension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
)

const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 5 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultIdleTimeout       = 60 * time.Second
)

var (
	_ catalogcollector.Extension = (*extension)(nil)
	_ catalogcollector.Readiness = (*extension)(nil)
)

type extension struct {
	log      *logrus.Entry
	cfg      *Config
	server   *http.Server
	listener net.Listener
	ready    atomic.Bool
}

// Ready marks the collector ready to serve its configured workload.
//
// Logging occurs only when the state changes, keeping repeated lifecycle calls
// idempotent and avoiding duplicate readiness messages.
func (e *extension) Ready() {
	if e.ready.CompareAndSwap(false, true) {
		e.log.Info("healthcheck marked ready")
	}
}

// NotReady marks the collector unable to serve its configured workload.
//
// Logging occurs only when the state changes. Calling NotReady from both the
// service and Shutdown is therefore safe and does not produce duplicate logs.
func (e *extension) NotReady() {
	if e.ready.CompareAndSwap(true, false) {
		e.log.Info("healthcheck marked not ready")
	}
}

// Start binds the healthcheck listener and starts serving requests.
//
// The listener is created synchronously so a bind failure is returned to the
// service before any source is started. The extension initially remains not
// ready; the service calls Ready after all source goroutines have entered
// their run wrappers.
func (e *extension) Start(
	_ context.Context,
	_ catalogcollector.Host,
) error {
	e.ready.Store(false)

	mux := http.NewServeMux()
	mux.HandleFunc(e.cfg.LivePath, e.handleLivez)
	mux.HandleFunc(e.cfg.ReadyPath, e.handleReadyz)

	listener, err := net.Listen("tcp", e.cfg.Endpoint)
	if err != nil {
		return fmt.Errorf(
			"listening on healthcheck endpoint %q: %w",
			e.cfg.Endpoint,
			err,
		)
	}

	e.listener = listener
	e.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}

	go e.serve()

	e.log.WithFields(logrus.Fields{
		"address":    listener.Addr().String(),
		"live_path":  e.cfg.LivePath,
		"ready_path": e.cfg.ReadyPath,
	}).Info("healthcheck server started")

	return nil
}

// serve runs the HTTP server until Shutdown is called or an unexpected server
// failure occurs. An unexpected failure marks the extension not ready.
func (e *extension) serve() {
	err := e.server.Serve(e.listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}

	e.NotReady()
	e.log.WithError(err).Error(
		"healthcheck server stopped unexpectedly",
	)
}

// Shutdown marks the extension not ready and gracefully stops the HTTP server.
func (e *extension) Shutdown(ctx context.Context) error {
	e.NotReady()
	e.log.Info("stopping healthcheck server")

	if e.server != nil {
		if err := e.server.Shutdown(ctx); err != nil {
			return fmt.Errorf(
				"shutting down healthcheck server: %w",
				err,
			)
		}
	}

	e.log.Info("healthcheck server stopped")
	return nil
}

func (e *extension) handleLivez(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !allowProbeRequest(w, r) {
		return
	}

	writeProbeResponse(
		w,
		r,
		http.StatusOK,
		"live",
	)
}

func (e *extension) handleReadyz(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !allowProbeRequest(w, r) {
		return
	}

	if e.ready.Load() {
		writeProbeResponse(
			w,
			r,
			http.StatusOK,
			"ready",
		)
		return
	}

	writeProbeResponse(
		w,
		r,
		http.StatusServiceUnavailable,
		"not_ready",
	)
}

// allowProbeRequest permits the standard probe methods and rejects all other
// methods without exposing configuration or internal component details.
func allowProbeRequest(
	w http.ResponseWriter,
	r *http.Request,
) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return true
	default:
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
}

// writeProbeResponse writes the small public health response.
//
// HEAD responses carry the same status and headers as GET responses but do not
// include a response body.
func writeProbeResponse(
	w http.ResponseWriter,
	r *http.Request,
	statusCode int,
	status string,
) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if r.Method == http.MethodHead {
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": status,
	})
}
