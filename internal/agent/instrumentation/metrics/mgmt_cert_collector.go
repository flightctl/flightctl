package metrics

import (
	"crypto/x509"
	"errors"

	mgmtcert "github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/management"
	mgmtcertcommon "github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/management/common"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Management cert current state
	mgmtCertLoadedGaugeName   = "flightctl_device_mgmt_cert_loaded"
	mgmtCertNotAfterGaugeName = "flightctl_device_mgmt_cert_not_after_timestamp_seconds"

	// Renewal flow metrics
	mgmtCertRenewalAttemptsName = "flightctl_device_mgmt_cert_renewal_attempts_total"
	mgmtCertRenewalDurationName = "flightctl_device_mgmt_cert_renewal_duration_seconds"

	mgmtCertLabelResult   = "result"
	mgmtCertResultSuccess = "success"
	mgmtCertResultFailure = "failure"
	mgmtCertResultPending = "pending"

	mgmtCertHelpLoaded          = "Whether a device management certificate is currently present/loaded (1) or not (0)."
	mgmtCertHelpNotAfter        = "Unix timestamp indicating the expiration time (NotAfter) of the current device management certificate."
	mgmtCertHelpRenewalAttempts = "Total number of device management certificate renewal attempts."
	mgmtCertHelpRenewalDuration = "Duration of a complete device management certificate renewal operation in seconds."
)

// MgmtCertCollector exposes device-scoped metrics for management certificate handling.
// It matches the ManagementCertMetricsCallback signature used by the certmanager middleware.
type MgmtCertCollector struct {
	log       *log.PrefixLogger
	loaded    prometheus.Gauge
	notAfter  prometheus.Gauge
	attempts  *prometheus.CounterVec
	durations *prometheus.HistogramVec
}

func NewMgmtCertCollector(l *log.PrefixLogger) *MgmtCertCollector {
	c := &MgmtCertCollector{log: l}

	c.loaded = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: mgmtCertLoadedGaugeName,
		Help: mgmtCertHelpLoaded,
	})

	c.notAfter = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: mgmtCertNotAfterGaugeName,
		Help: mgmtCertHelpNotAfter,
	})

	c.attempts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: mgmtCertRenewalAttemptsName,
		Help: mgmtCertHelpRenewalAttempts,
	}, []string{mgmtCertLabelResult})

	c.durations = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    mgmtCertRenewalDurationName,
		Help:    mgmtCertHelpRenewalDuration,
		Buckets: prometheus.DefBuckets,
	}, []string{mgmtCertLabelResult})

	// Pre-create all label sets so series exist on first scrape.
	_ = c.attempts.WithLabelValues(mgmtCertResultSuccess)
	_ = c.attempts.WithLabelValues(mgmtCertResultFailure)
	_ = c.attempts.WithLabelValues(mgmtCertResultPending)

	_ = c.durations.WithLabelValues(mgmtCertResultSuccess)
	_ = c.durations.WithLabelValues(mgmtCertResultFailure)
	_ = c.durations.WithLabelValues(mgmtCertResultPending)

	// Default state until startup explicitly reports current cert presence.
	c.loaded.Set(0)
	c.notAfter.Set(0)

	return c
}

// Observe records a single management certificate event.
//
// Rules:
//
//		kind == current:
//		- MUST set loaded=1 and not_after if cert != nil
//		- MUST set loaded=0 and not_after=0 if cert == nil
//		- MUST NOT create attempts/duration
//
//		kind == renewal:
//		- attempts + duration
//		- update loaded/not_after ONLY on success with cert != nil (we do not clear state on failure)
//
//		Result mapping (renewal only):
//	  	- err == nil                               → success
//	  	- ErrProvisionNotReady                     → pending
//	  	- any other error                          → failure
func (c *MgmtCertCollector) Observe(kind mgmtcertcommon.ManagementCertEventKind, cert *x509.Certificate, durationSeconds float64, err error) {
	switch kind {
	case mgmtcertcommon.ManagementCertEventKindCurrent:
		// Startup/current: explicit state reporting.
		if cert == nil {
			c.loaded.Set(0)
			c.notAfter.Set(0)
			return
		}
		c.loaded.Set(1)
		c.notAfter.Set(float64(cert.NotAfter.Unix()))
		return

	case mgmtcertcommon.ManagementCertEventKindRenewal:
		// Renewal: attempts + duration.
		var result string
		switch {
		case err == nil:
			result = mgmtCertResultSuccess
		case errors.Is(err, mgmtcert.ErrProvisionNotReady):
			result = mgmtCertResultPending
		default:
			result = mgmtCertResultFailure
		}

		c.attempts.WithLabelValues(result).Inc()
		if durationSeconds > 0 {
			c.durations.WithLabelValues(result).Observe(durationSeconds)
		}

		// On success, we expect cert != nil and we can refresh state.
		if err == nil && cert != nil {
			c.loaded.Set(1)
			c.notAfter.Set(float64(cert.NotAfter.Unix()))
		}
		return

	default:
		// Unknown kind: do nothing.
		return
	}
}

func (c *MgmtCertCollector) Describe(ch chan<- *prometheus.Desc) {
	c.loaded.Describe(ch)
	c.notAfter.Describe(ch)
	c.attempts.Describe(ch)
	c.durations.Describe(ch)
}

func (c *MgmtCertCollector) Collect(ch chan<- prometheus.Metric) {
	c.loaded.Collect(ch)
	c.notAfter.Collect(ch)
	c.attempts.Collect(ch)
	c.durations.Collect(ch)
}
