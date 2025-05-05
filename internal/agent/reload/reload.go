package reload

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/pkg/log"
)

const reloadTimeout = 60 * time.Second

type Callback func(ctx context.Context, config *config.Config) error

type Manager interface {
	Run(context.Context)
	Reload(context.Context)
	Register(Callback)
}

// NewManager creates a new reload manager.
func NewManager(configFile string, log *log.PrefixLogger) Manager {
	return &manager{
		log:        log,
		configFile: configFile,
	}
}

type manager struct {
	mu        sync.Mutex
	callbacks []Callback

	configFile string
	log        *log.PrefixLogger
}

func (m *manager) Run(ctx context.Context) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP)
	defer signal.Stop(signals)
	for {
		select {
		case <-signals:
			m.log.Info("Agent received SIGHUP signal")
			m.Reload(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (m *manager) Reload(ctx context.Context) {
	m.log.Info("Reloading agent config")
	defer m.log.Info("Agent config reloaded")

	timeoutCtx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()

	cfg, err := config.Load(m.configFile)
	if err != nil {
		m.log.Errorf("failed to load config: %v", err)
		return
	}

	m.mu.Lock()
	registered := make([]Callback, len(m.callbacks))
	copy(registered, m.callbacks)
	m.mu.Unlock()

	for _, f := range registered {
		if err := f(timeoutCtx, cfg); err != nil {
			m.log.Errorf("Failed to reload device: %v", err)
		}
	}
}

func (m *manager) Register(c Callback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks = append(m.callbacks, c)
}
