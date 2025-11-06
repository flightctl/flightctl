package metrics

import (
	"strings"
	"sync"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

const (
	labelResult    = "result"
	suffixDuration = "_duration"
	suffixSeconds  = "_duration_seconds"
	helpDefault    = "RPC latency histogram in seconds"
)

var defaultOps = []string{
	"create_enrollmentrequest",
	"get_enrollmentrequest",
	"get_rendered_device_spec",
	"update_device_status",
	"patch_device_status",
	"create_certificate_signing_request",
	"get_certificate_signing_request",
}

type RPCCollector struct {
	log    *log.PrefixLogger
	mu     sync.RWMutex
	histos map[string]*prometheus.HistogramVec
	opsSet map[string]struct{}
}

// NewRPCCollector constructs a metrics collector dedicated to RPC latency.
// It exposes a fixed set of per-operation histograms (<op>_duration_seconds)
// labeled by result=("success"|"error"). The returned collector provides
// Observe(operation, seconds, err) to plug directly into the agent’s RPC
// timing callback.
func NewRPCCollector(l *log.PrefixLogger) *RPCCollector {
	opsSet := make(map[string]struct{}, len(defaultOps))
	for _, op := range defaultOps {
		opsSet[op] = struct{}{}
	}

	histos := make(map[string]*prometheus.HistogramVec, len(opsSet))
	for op := range opsSet {
		name := op + suffixSeconds
		hv := prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    name,
			Help:    helpDefault,
			Buckets: prometheus.DefBuckets,
		}, []string{labelResult})

		// Pre-create both label sets so series exist on first scrape.
		_ = hv.WithLabelValues("success")
		_ = hv.WithLabelValues("error")

		histos[name] = hv
	}

	return &RPCCollector{
		log:    l,
		histos: histos,
		opsSet: opsSet,
	}
}

// Observe is the drop-in hook for the agent’s RPC timing callback.
// It matches the callback signature (operation, seconds, err) and records
// latency under <operation>_duration_seconds with result="success|error".
func (c *RPCCollector) Observe(operation string, seconds float64, err error) {
	op := canonicalizeOp(operation)
	if op == "" {
		return
	}

	c.mu.RLock()
	_, allowed := c.opsSet[op]
	hv := c.histos[op+suffixSeconds]
	c.mu.RUnlock()

	if !allowed || hv == nil {
		c.log.WithFields(logrus.Fields{
			"operation": operation,
		}).Error("unknown RPC metric operation")
		return
	}

	result := "success"
	if err != nil {
		result = "error"
	}
	hv.WithLabelValues(result).Observe(seconds)
}

func (c *RPCCollector) Describe(ch chan<- *prometheus.Desc) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, hv := range c.histos {
		hv.Describe(ch)
	}
}

func (c *RPCCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, hv := range c.histos {
		hv.Collect(ch)
	}
}

// canonicalizeOp lowercases, trims, and strips optional suffixes,
// returning the canonical operation id.
func canonicalizeOp(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(s, suffixSeconds):
		return strings.TrimSuffix(s, suffixSeconds)
	case strings.HasSuffix(s, suffixDuration):
		return strings.TrimSuffix(s, suffixDuration)
	default:
		return s
	}
}
