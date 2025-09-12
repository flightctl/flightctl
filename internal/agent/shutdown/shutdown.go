package shutdown

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	shutdownScheduledPath = "/run/systemd/shutdown/scheduled"
	runlevelPath          = "/run/utmp"

	runlevel0 = "runlevel 0"
	runlevel6 = "runlevel 6"
)

type Manager interface {
	Run(context.Context)
	Register(string, func(context.Context) error)
}

type manager struct {
	once       sync.Once
	registered map[string]func(context.Context) error
	cancelFn   context.CancelFunc
	timeout    time.Duration
	log        *log.PrefixLogger
}

// NewManager creates a new shutdown manager.
func NewManager(log *log.PrefixLogger, timeout time.Duration, cancelFn context.CancelFunc) Manager {
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
			m.shutdown(ctx)
			m.cancelFn()
			close(signals)
		case <-ctx.Done():
			m.log.Infof("Context has been cancelled, shutting down.")
			m.shutdown(ctx)
			close(signals)
		}
	}(ctx)

	<-ctx.Done()
}

func (m *manager) shutdown(ctx context.Context) {
	// ensure multiple calls to Shutdown are idempotent
	m.once.Do(func() {
		now := time.Now()
		// give the agent time to shutdown gracefully
		ctx, cancel := context.WithTimeout(ctx, m.timeout)
		defer cancel()
		for name, fn := range m.registered {
			m.log.Debugf("Executing shutdown handler: %s", name)
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

// IsSystemShutdown checks if the system is shutting down or rebooting
// by checking systemd targets and runlevels
func IsSystemShutdown(ctx context.Context, systemdClient *client.Systemd, reader fileio.Reader, log *log.PrefixLogger) bool {
	switch {
	case isShuttingDownViaRunlevel(reader, log):
		return true
	case isShuttingDownViaSystemd(ctx, systemdClient, reader, log):
		return true
	default:
		return false
	}
}

func isShuttingDownViaSystemd(ctx context.Context, systemdClient *client.Systemd, reader fileio.Reader, log *log.PrefixLogger) bool {
	exists, err := reader.PathExists(shutdownScheduledPath)
	if err == nil && exists {
		log.Info("System shutdown detected via scheduled file")
		return true
	}

	shutdownJobs, err := systemdClient.ListJobs(ctx)
	if err != nil {
		log.Debugf("Failed to list systemd jobs: %v", err)
		return false
	}

	// check if any shutdown-related jobs are starting
	shutdownTargets := map[string]struct{}{
		"shutdown.target": {},
		"reboot.target":   {},
		"poweroff.target": {},
		"halt.target":     {},
	}

	for _, job := range shutdownJobs {
		if _, isShutdownTarget := shutdownTargets[job.Unit]; isShutdownTarget && job.JobType == "start" {
			log.Infof("System shutdown detected: %s job is %s", job.Unit, job.State)
			return true
		}
	}

	return false
}

func isShuttingDownViaRunlevel(reader fileio.Reader, log *log.PrefixLogger) bool {
	runlevelBytes, err := reader.ReadFile(runlevelPath)
	if err == nil {
		runlevel := string(runlevelBytes)
		// runlevel 0 = halt, 6 = reboot
		if strings.Contains(runlevel, runlevel0) || strings.Contains(runlevel, runlevel6) {
			log.Infof("System shutdown detected: runlevel change")
			return true
		}
	}

	return false
}
