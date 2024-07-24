package hook

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	DefaultHookActionTimeout = 10 * time.Second
)

var _ Manager = (*HookManager)(nil)

type HookManager struct {
	config *ConfigHook
	os     Hook
	exec   executer.Executer

	log *log.PrefixLogger
}

// NewManager creates a new hook manager.
func NewManager(log *log.PrefixLogger, exec executer.Executer) (Manager, error) {
	configHook, err := newConfigHook(log, exec)
	if err != nil {
		return nil, err
	}

	return &HookManager{
		config: configHook,
		exec:   exec,
		log:    log,
	}, nil
}

// Run starts the hook manager and listens for events.
func (m *HookManager) Run(ctx context.Context) {
	go m.Config().Post().Run(ctx)
	go m.Config().Pre().Run(ctx)

	// TODO: implement the OS hooks

	<-ctx.Done()
}

func (m *HookManager) Config() *ConfigHook {
	return m.config
}

func (m *HookManager) OS() Hook {
	return m.os
}
