package pprof

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	pprofPortDefault = 15689
	// /debug/pprof/profile
	pprofCPUCapDefault = 30 * time.Second
	// /debug/pprof/trace
	pprofTraceCapDefault = 5 * time.Second
)

const (
	httpGracefulShutdownTimeout = 5 * time.Second
	httpReadHeaderTimeout       = 2 * time.Second
	httpReadTimeout             = 5 * time.Second
	httpIdleTimeout             = 60 * time.Second
)

// PprofServer is a localhost-only HTTP server exposing Go runtime profiling endpoints.
type PprofServer struct {
	log  logrus.FieldLogger
	opts pprofOptions
}

// PprofOption configures a PprofServer.
type PprofOption func(*pprofOptions)

type pprofOptions struct {
	port     int
	cpuCap   time.Duration
	traceCap time.Duration
}

// WithPort sets the TCP port for the pprof server. Only positive values are applied.
// Default: 15689
func WithPort(port int) PprofOption {
	return func(o *pprofOptions) {
		if port > 0 {
			o.port = port
		}
	}
}

// WithCPUCap sets the maximum duration for CPU profiling via /debug/pprof/profile.
// Only positive durations are applied. Default: 30s
func WithCPUCap(d time.Duration) PprofOption {
	return func(o *pprofOptions) {
		if d > 0 {
			o.cpuCap = d
		}
	}
}

// WithTraceCap sets the maximum duration for execution tracing via /debug/pprof/trace.
// Only positive durations are applied. Default: 5s
func WithTraceCap(d time.Duration) PprofOption {
	return func(o *pprofOptions) {
		if d > 0 {
			o.traceCap = d
		}
	}
}

// NewPprofServer creates a new pprof server with the given logger.
// The server binds exclusively to 127.0.0.1 for security.
func NewPprofServer(log logrus.FieldLogger) *PprofServer {
	return &PprofServer{log: log, opts: defaultPprofOptions()}
}

// Run starts the pprof HTTP server and blocks until the provided context is canceled
// or an error occurs. Optional settings can be supplied via PprofOption.
func (p *PprofServer) Run(ctx context.Context, options ...PprofOption) error {
	opts := p.opts
	for _, fn := range options {
		if fn != nil {
			fn(&opts)
		}
	}
	// Build mux with all standard endpoints under /debug/pprof/*
	mux := http.NewServeMux()

	// Index & helpers
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)

	// Profiles (cap CPU/trace via query rewrite)
	mux.HandleFunc("/debug/pprof/profile", capSeconds(pprof.Profile, opts.cpuCap))
	mux.HandleFunc("/debug/pprof/trace", capSeconds(pprof.Trace, opts.traceCap))
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))

	// Server pinned to loopback (host not configurable)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      computeWriteTimeout(opts),
		IdleTimeout:       httpIdleTimeout,
	}

	go func() {
		<-ctx.Done()
		if p.log != nil {
			p.log.WithError(ctx.Err()).Info("pprof: shutdown signal received")
		}
		ctxTimeout, cancel := context.WithTimeout(context.Background(), httpGracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		if err := srv.Shutdown(ctxTimeout); err != nil && p.log != nil {
			p.log.WithError(err).Warn("pprof: server shutdown error")
		}
	}()

	if p.log != nil {
		p.log.Infof("pprof listening on http://%s/debug/pprof/", addr)
	}

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// capSeconds ensures the handler always runs with a bounded "seconds":
func capSeconds(h http.HandlerFunc, capDur time.Duration) http.HandlerFunc {
	capS := int(capDur / time.Second) // floor to whole seconds
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		secStr := q.Get("seconds")
		if v, err := strconv.Atoi(secStr); err != nil || v <= 0 || v > capS {
			q.Set("seconds", strconv.Itoa(capS))
			r.URL.RawQuery = q.Encode()
		}
		h.ServeHTTP(w, r)
	}
}

func defaultPprofOptions() pprofOptions {
	return pprofOptions{
		port:     pprofPortDefault,
		cpuCap:   pprofCPUCapDefault,
		traceCap: pprofTraceCapDefault,
	}
}

func computeWriteTimeout(opts pprofOptions) time.Duration {
	longest := opts.cpuCap
	if opts.traceCap > longest {
		longest = opts.traceCap
	}
	return longest + 5*time.Second
}
