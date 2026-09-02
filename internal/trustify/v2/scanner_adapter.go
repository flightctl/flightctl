package trustifyv2

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/vulnerability"
	"github.com/sirupsen/logrus"
)

func init() {
	vulnerability.Register(string(config.VulnerabilityBackendTrustify), func(cfg *config.VulnerabilityConfig) (vulnerability.Scanner, error) {
		return NewScanner(cfg.Trustify)
	}, vulnerability.WithSBOMUpload(true))
}

// trustifyScanner implements vulnerability.Scanner over the Trustify v2 client,
// converting Trustify findings into backend-agnostic vulnerability.Finding DTOs.
type trustifyScanner struct {
	cfg *config.TrustifyConfig

	// mu guards lazy client construction so overlapping ScanImages calls
	// (VulnerabilitySync is SystemWide and can be rescheduled before a prior
	// run completes) cannot race to build duplicate clients.
	mu     sync.Mutex
	client VulnerabilityClient
}

// NewScanner returns a Trustify-backed Scanner. It returns nil, nil when cfg is
// nil so callers can treat a missing Trustify configuration as "no scanner".
// The underlying client is created lazily on first ScanImages because the
// scanner factory has no request context, whereas client construction (OIDC
// discovery in client-credentials mode) needs one.
func NewScanner(cfg *config.TrustifyConfig) (vulnerability.Scanner, error) {
	if cfg == nil {
		return nil, nil
	}
	return &trustifyScanner{cfg: cfg}, nil
}

// newVulnerabilityClient is a seam so tests can exercise concurrent lazy
// initialization without real network I/O.
var newVulnerabilityClient = NewVulnerabilityClient

func (s *trustifyScanner) ensureClient(ctx context.Context) (VulnerabilityClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A client already built (or injected at construction in tests) is reused.
	if s.client != nil {
		return s.client, nil
	}
	// Construction (OIDC discovery in client-credentials mode) can fail
	// transiently on a canceled context or a network blip. Return the error
	// without caching it so a later scan retries rather than staying wedged
	// until process restart.
	client, err := newVulnerabilityClient(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	s.client = client
	return s.client, nil
}

// ScanImages fetches findings for the given images' digests. Trustify keys
// purely on digest, so the image reference is ignored. Findings whose status or
// severity cannot be normalized are skipped so a single malformed finding does
// not fail the whole scan.
func (s *trustifyScanner) ScanImages(ctx context.Context, images []vulnerability.ImageRef) (map[string][]vulnerability.Finding, error) {
	client, err := s.ensureClient(ctx)
	if err != nil {
		return nil, err
	}

	digests := make([]string, 0, len(images))
	for _, img := range images {
		digests = append(digests, img.Digest)
	}

	raw, err := client.GetVulnerabilitiesForDigests(ctx, digests)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]vulnerability.Finding, len(raw))
	skipped := 0
	for digest, findings := range raw {
		if findings == nil {
			out[digest] = nil
			continue
		}
		converted := make([]vulnerability.Finding, 0, len(findings))
		for _, f := range findings {
			vf, err := toVulnFinding(f)
			if err != nil {
				logrus.WithError(err).Debugf("Skipping finding for digest %s cve %s", f.ImageDigest, f.CVEID)
				skipped++
				continue
			}
			converted = append(converted, vf)
		}
		out[digest] = converted
	}
	// Surface data-quality issues at warn level so they are visible without
	// debug logging; per-finding detail stays at debug above.
	if skipped > 0 {
		logrus.Warnf("Skipped %d Trustify findings due to normalization errors", skipped)
	}
	return out, nil
}

func toVulnFinding(f Finding) (vulnerability.Finding, error) {
	status, err := normalizeTrustifyStatus(f.Status)
	if err != nil {
		return vulnerability.Finding{}, err
	}
	severity, err := normalizeTrustifySeverity(f.Severity)
	if err != nil {
		return vulnerability.Finding{}, err
	}

	vf := vulnerability.Finding{
		CveID:       f.CVEID,
		ImageDigest: f.ImageDigest,
		Status:      status,
		Severity:    severity,
		CvssScore:   f.CVSSScore,
		Description: f.Description,
		PublishedAt: f.PublishedAt,
	}
	if f.AdvisoryID != "" {
		vf.AdvisoryID = &f.AdvisoryID
	}
	if f.Issuer != nil {
		vf.Issuer = &vulnerability.Issuer{
			ID:      f.Issuer.Id.String(),
			Name:    f.Issuer.Name,
			CpeKey:  f.Issuer.CpeKey,
			Website: f.Issuer.Website,
		}
	}
	return vf, nil
}

// normalizeTrustifyStatus maps a Trustify status onto the canonical status
// string. The returned values must match model.VulnerabilityStatus so the sync
// task can convert them without re-validating.
func normalizeTrustifyStatus(status string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "affected":
		return "affected", nil
	case "not_affected":
		return "not_affected", nil
	case "fixed":
		return "fixed", nil
	case "unknown", "":
		return "unknown", nil
	default:
		return "", fmt.Errorf("unsupported trustify status %q", status)
	}
}

// normalizeTrustifySeverity maps a Trustify severity onto the canonical
// severity string. The returned values must match model.VulnerabilitySeverity
// so the sync task can convert them without re-validating.
func normalizeTrustifySeverity(severity string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return "Critical", nil
	case "high":
		return "High", nil
	case "medium":
		return "Medium", nil
	case "low":
		return "Low", nil
	case "none":
		return "None", nil
	case "unknown", "":
		return "Unknown", nil
	default:
		return "", fmt.Errorf("unsupported trustify severity %q", severity)
	}
}
