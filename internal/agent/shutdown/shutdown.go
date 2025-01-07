package shutdown

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
)

type Manager interface {
	Run(context.Context)
	Shutdown(context.Context)
	Register(string, func(context.Context) error)
}

type manager struct {
	once       sync.Once
	registered map[string]func(context.Context) error
	cancelFn   context.CancelFunc
	timeout    time.Duration
	log        *log.PrefixLogger
}

func New(log *log.PrefixLogger, timeout time.Duration, cancelFn context.CancelFunc) Manager {
	return &manager{
		registered: make(map[string]func(context.Context) error),
		timeout:    timeout,
		cancelFn:   cancelFn,
		log:        log,
	}
}

func (m *manager) Run(ctx context.Context) {
	defer m.log.Infof("Agent shutdown complete")
	// handle teardown
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func(ctx context.Context) {
		select {
		case s := <-signals:
			m.log.Infof("Agent received shutdown signal: %s", s)
			m.Shutdown(ctx)
			m.cancelFn()
			close(signals)
		case <-ctx.Done():
			m.log.Infof("Context has been cancelled, shutting down.")
			m.Shutdown(ctx)
			close(signals)
		}
	}(ctx)

	<-ctx.Done()
}

func (m *manager) Shutdown(ctx context.Context) {
	// ensure multiple calls to Shutdown are idempotent
	m.once.Do(func() {
		now := time.Now()
		// give the agent time to shutdown gracefully
		ctx, cancel := context.WithTimeout(ctx, m.timeout)
		defer cancel()
		for name, fn := range m.registered {
			m.log.Infof("Shutting down: %s", name)
			if err := fn(ctx); err != nil {
				m.log.Errorf("Error shutting down: %s", err)
			}
		}
		m.log.Infof("Shutdown complete in %s", time.Since(now))
	})
}

func (m *manager) Register(name string, fn func(context.Context) error) {
	if _, ok := m.registered[name]; ok {
		m.log.Warnf("Shutdown function %s already registered", name)
		return
	}
	m.registered[name] = fn
}
