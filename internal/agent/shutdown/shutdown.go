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
	// shutdown targets
	shutdownTarget = "shutdown.target"
	rebootTarget   = "reboot.target"
	poweroffTarget = "poweroff.target"
	haltTarget     = "halt.target"

	shutdownScheduledPath = "/run/systemd/shutdown/scheduled"
	runlevelPath          = "/run/utmp"
	procDir               = "/proc"

	runlevel0 = "runlevel 0"
	runlevel6 = "runlevel 6"
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

// IsSystemShutdown checks if the system is shutting down or rebooting
// by checking systemd targets and runlevels
func IsSystemShutdown(ctx context.Context, systemdClient *client.Systemd, reader fileio.Reader, log *log.PrefixLogger) bool {
	switch {
	case isShuttingDownViaRunlevel(reader, log):
		return true
	case isShuttingDownViaProcess(reader, log):
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
		log.Info("System shutdown detected")
		return true
	}

	shutdownTargets := []string{
		shutdownTarget,
		rebootTarget,
		poweroffTarget,
		haltTarget,
	}

	for _, target := range shutdownTargets {
		active, err := systemdClient.IsActive(ctx, target)
		if err != nil {
			log.Debugf("Failed to check if %s is active: %v", target, err)
			continue
		}

		if active {
			log.Infof("System shutdown detected: %s is active", target)
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

func isShuttingDownViaProcess(reader fileio.Reader, log *log.PrefixLogger) bool {
	entries, err := reader.ReadDir(procDir)
	if err != nil {
		return false
	}

	standaloneShutdownCommands := []string{"shutdown", "reboot", "poweroff", "halt"}
	systemctlSubcommands := []string{"poweroff", "reboot", "halt"}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// check if directory name is a PID
		pid := entry.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}

		cmdlineFile := procDir + "/" + pid + "/cmdline"
		cmdlineBytes, err := reader.ReadFile(cmdlineFile)
		if err != nil {
			continue
		}

		args := strings.Split(string(cmdlineBytes), "\x00")
		for i, arg := range args {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				continue
			}

			baseName := arg
			if lastSlash := strings.LastIndex(arg, "/"); lastSlash != -1 {
				baseName = arg[lastSlash+1:]
			}

			for _, cmd := range standaloneShutdownCommands {
				if baseName == cmd {
					log.Infof("System shutdown detected: %s command found in process list", cmd)
					return true
				}
			}

			if baseName == "systemctl" {
				// Check the next argument for shutdown subcommands
				if i+1 < len(args) {
					nextArg := strings.TrimSpace(args[i+1])
					for _, subcmd := range systemctlSubcommands {
						if nextArg == subcmd {
							log.Infof("System shutdown detected: systemctl %s command found in process list", subcmd)
							return true
						}
					}
				}
			}
		}
	}

	return false
}
