package common

import "crypto/x509"

type ManagementCertEventKind string

const (
	// Emitted when we load the currently-installed certificate (e.g., agent restart).
	ManagementCertEventKindCurrent ManagementCertEventKind = "current"
	// Emitted for a renewal flow (provision/store). Exactly one event per renewal attempt.
	ManagementCertEventKindRenewal ManagementCertEventKind = "renewal"
)

// ManagementCertMetricsCallback observes management certificate events.
//
// Semantics:
//   - kind=current: best-effort observation of the currently installed cert (no renewal attempt).
//     durationSeconds is expected to be 0.
//   - kind=renewal: one renewal attempt observation (success/pending/failure) + durationSeconds.
//     pending is typically signaled via ErrProvisionNotReady.
//   - cert may be nil (e.g., failures or non-ready/pending).
type ManagementCertMetricsCallback func(
	kind ManagementCertEventKind,
	cert *x509.Certificate,
	durationSeconds float64,
	err error,
)
