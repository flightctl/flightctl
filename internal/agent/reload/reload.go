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

type manager struct {
	mu             sync.Mutex
	reloadMu       sync.Mutex
	callbacks      []Callback
	baseConfigFile string
	configDir      string
	log            *log.PrefixLogger
}

func (m *manager) Run(ctx context.Context) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP)
	defer signal.Stop(signals)
	for {
		select {
		case <-signals:
			m.Reload(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (m *manager) Reload(ctx context.Context) {
	m.log.Info("reloading config")
	defer m.log.Info("config reloaded")
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	timeoutCtx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()

	r := newReader(m.baseConfigFile, m.configDir)
	cfg, err := r.readConfig()
	if err != nil {
		m.log.Errorf("failed to read config: %v", err)
		return
	}
	registered := m.callbacks
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

func New(baseConfigFile, configDir string, log *log.PrefixLogger) Manager {
	return &manager{
		log:            log,
		configDir:      configDir,
		baseConfigFile: baseConfigFile,
	}
}
