package instrumentation

import (
	"context"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	coreinst "github.com/flightctl/flightctl/internal/instrumentation/pprof"
	"github.com/flightctl/flightctl/pkg/log"
)

// pprofServer plugs the core pprof server into the agent's lifecycle.
type pprofServer struct {
	log *log.PrefixLogger
	srv *coreinst.PprofServer
}

// Run blocks until ctx is canceled
func (p *pprofServer) Run(ctx context.Context) {
	if p.srv == nil {
		if p.log != nil {
			p.log.Debugf("pprof disabled")
		}
		return
	}
	if err := p.srv.Run(ctx); err != nil {
		if p.log != nil {
			p.log.WithError(err).Error("pprof server exited")
		}
	}
}

// NewPprofServer wires a core PprofServer if profiling is enabled.
func NewPprofServer(l *log.PrefixLogger, cfg *agent_config.Config) *pprofServer {
	ps := &pprofServer{log: l}

	if cfg == nil || !cfg.ProfilingEnabled {
		return ps
	}

	ps.srv = coreinst.NewPprofServer(l)
	return ps
}
