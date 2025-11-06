package instrumentation

import (
	"context"
	"sync"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	agentmetrics "github.com/flightctl/flightctl/internal/agent/instrumentation/metrics"
	instmetrics "github.com/flightctl/flightctl/internal/instrumentation/metrics"
	instpprof "github.com/flightctl/flightctl/internal/instrumentation/pprof"
	"github.com/flightctl/flightctl/pkg/log"
)

type metricsServer struct {
	log *log.PrefixLogger
	srv *instmetrics.MetricsServer
}

// NewMetricsServer wires a core metrics server if metrics are enabled.
// It exposes /metrics on loopback and registers any configured collectors/callbacks.
func NewMetricsServer(l *log.PrefixLogger, cfg *agent_config.Config) *metricsServer {
	ms := &metricsServer{log: l}
	if cfg == nil || !cfg.MetricsEnabled {
		return ms
	}

	rpcCollector := agentmetrics.NewRPCCollector(l)

	// Provide the Observe hook to producers (enrollment/management RPCs).
	cfg.SetEnrollmentMetricsCallback(rpcCollector.Observe)
	cfg.SetManagementMetricsCallback(rpcCollector.Observe) // <- was duplicated earlier

	ms.srv = instmetrics.NewMetricsServer(l, rpcCollector)
	return ms
}

// Run blocks until ctx is canceled.
func (m *metricsServer) Run(ctx context.Context) {
	if m.srv == nil {
		if m.log != nil {
			m.log.Debugf("metrics disabled")
		}
		return
	}
	if err := m.srv.Run(ctx); err != nil {
		if m.log != nil {
			m.log.WithError(err).Error("metrics server exited")
		}
	}
}

// pprofServer plugs the core pprof server into the agent's lifecycle.
type pprofServer struct {
	log *log.PrefixLogger
	srv *instpprof.PprofServer
}

// NewPprofServer wires a core pprof server if profiling is enabled.
// It exposes /debug/pprof on loopback.
func NewPprofServer(l *log.PrefixLogger, cfg *agent_config.Config) *pprofServer {
	ps := &pprofServer{log: l}
	if cfg == nil || !cfg.ProfilingEnabled {
		return ps
	}
	ps.srv = instpprof.NewPprofServer(l)
	return ps
}

// Run blocks until ctx is canceled.
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

// AgentInstrumentation is a thin facade that runs metrics + pprof in parallel.
type AgentInstrumentation struct {
	metrics *metricsServer
	pprof   *pprofServer
	log     *log.PrefixLogger
}

// NewAgentInstrumentation builds the agent’s observability instrumentation (e.g., metrics, pprof).
func NewAgentInstrumentation(l *log.PrefixLogger, cfg *agent_config.Config) *AgentInstrumentation {
	return &AgentInstrumentation{
		metrics: NewMetricsServer(l, cfg),
		pprof:   NewPprofServer(l, cfg),
		log:     l,
	}
}

// Run starts all observability components (e.g., metrics, pprof) and blocks until ctx is canceled.
func (ai *AgentInstrumentation) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() { defer wg.Done(); ai.metrics.Run(ctx) }()
	go func() { defer wg.Done(); ai.pprof.Run(ctx) }()

	wg.Wait()
}
