package trustifyv2

import (
	"context"
	"fmt"
	"strings"

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
	cfg    *config.TrustifyConfig
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

func (s *trustifyScanner) ensureClient(ctx context.Context) (VulnerabilityClient, error) {
	if s.client != nil {
		return s.client, nil
	}
	client, err := NewVulnerabilityClient(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	s.client = client
	return client, nil
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
				continue
			}
			converted = append(converted, vf)
		}
		out[digest] = converted
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
