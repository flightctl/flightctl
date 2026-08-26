package quay

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/vulnerability"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// statusAffected is the only status Quay-sourced findings carry: Quay reports
// vulnerabilities currently detected in an image and provides no VEX status.
const statusAffected = "affected"

func init() {
	vulnerability.Register(string(config.VulnerabilityBackendQuay),
		func(cfg *config.VulnerabilityConfig) (vulnerability.Scanner, error) {
			return NewScanner(cfg.Quay, nil)
		}, vulnerability.WithSBOMUpload(false)) // Quay indexes images natively
}

// quayScanner implements vulnerability.Scanner over the Quay Security API
// client, converting Quay/Clair scan reports into backend-agnostic
// vulnerability.Finding DTOs.
type quayScanner struct {
	client *Client
	log    logrus.FieldLogger
}

// NewScanner returns a Quay-backed Scanner. It returns (nil, nil) when cfg is
// nil so callers can treat a missing Quay configuration as "no scanner". Unlike
// Trustify, the Quay client needs no request context to construct (bearer-token
// auth, no OIDC discovery), so it is built eagerly here.
func NewScanner(cfg *config.QuayConfig, log logrus.FieldLogger) (vulnerability.Scanner, error) {
	if cfg == nil {
		return nil, nil
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	client, err := NewClient(cfg, log)
	if err != nil {
		return nil, err
	}
	return &quayScanner{client: client, log: log}, nil
}

// ScanImages fetches each image's Quay Security report and converts the
// contained vulnerabilities into findings keyed by image digest. Images the
// client skips (missing reference, other registry, not "scanned", 404)
// contribute no findings; a genuine fetch or decode error aborts the scan.
func (s *quayScanner) ScanImages(ctx context.Context, images []vulnerability.ImageRef) (map[string][]vulnerability.Finding, error) {
	out := make(map[string][]vulnerability.Finding)
	for _, image := range images {
		report, err := s.client.FetchImageSecurity(ctx, image)
		if err != nil {
			return nil, fmt.Errorf("scanning image %s: %w", image.Digest, err)
		}
		if report == nil {
			continue
		}
		findings := findingsFromReport(image.Digest, report, s.log)
		if len(findings) > 0 {
			out[image.Digest] = append(out[image.Digest], findings...)
		}
	}
	return out, nil
}

// cveRegex matches CVE identifiers in free text (e.g. advisory links). It
// mirrors the pattern Quay uses internally to derive CVEs from vulnerability
// links.
var cveRegex = regexp.MustCompile(`CVE-\d{4}-\d{4,7}`)

// namespaceToIssuer maps a Quay NamespaceName prefix (the OS distribution whose
// vulnerability database was queried) to the advisory publisher RHEM displays.
var namespaceToIssuer = map[string]string{
	"rhel":   "Red Hat",
	"centos": "Red Hat",
	"debian": "Debian",
	"ubuntu": "Ubuntu",
	"alpine": "Alpine",
	"amzn":   "Amazon",
	"oracle": "Oracle",
}

// severityMap maps Quay/Clair severity values to canonical RHEM severities.
// Values must match model.VulnerabilitySeverity so the sync task can cast them
// without re-validating. Unrecognized values fall back to "Unknown".
var severityMap = map[string]string{
	"Critical":   "Critical",
	"Defcon1":    "Critical",
	"High":       "High",
	"Medium":     "Medium",
	"Low":        "Low",
	"Negligible": "None",
	"Unknown":    "Unknown",
}

// findingsFromReport walks a scanned Quay report's Features and their
// Vulnerabilities and returns one Finding per extracted CVE for the given image
// digest. Vulnerabilities with no extractable CVE ID are skipped with a debug
// log (RHEM's composite key requires a CVE ID). Findings are deduplicated by
// (digest, cve_id): the first occurrence of a CVE is authoritative, so a CVE
// referenced from multiple Features collapses to a single finding carrying that
// first occurrence's fields.
func findingsFromReport(digest string, report *Response, log logrus.FieldLogger) []vulnerability.Finding {
	if report == nil || report.Data == nil || report.Data.Layer == nil {
		return nil
	}
	if log == nil {
		log = logrus.StandardLogger()
	}

	var findings []vulnerability.Finding
	seen := make(map[string]struct{})
	for _, feature := range report.Data.Layer.Features {
		for _, vuln := range feature.Vulnerabilities {
			cveIDs := extractCVEIDs(vuln)
			if len(cveIDs) == 0 {
				log.WithFields(logrus.Fields{
					"digest": digest,
					"name":   vuln.Name,
				}).Debug("skipping vulnerability with no extractable CVE ID")
				continue
			}
			for _, cveID := range cveIDs {
				if _, ok := seen[cveID]; ok {
					continue
				}
				seen[cveID] = struct{}{}
				findings = append(findings, vulnerability.Finding{
					CveID:       cveID,
					ImageDigest: digest,
					Severity:    mapSeverity(vuln.Severity),
					CvssScore:   cvssScore(vuln.Metadata),
					Status:      statusAffected,
					Issuer:      buildIssuer(vuln.NamespaceName),
					AdvisoryID:  advisoryID(vuln.Name),
					Description: vuln.Description,
				})
			}
		}
	}
	return findings
}

// extractCVEIDs returns the CVE identifiers for a vulnerability. It matches the
// Link first (where Quay places advisory references, which may fan out to
// several CVEs), falling back to the Name for entries such as Debian and Alpine
// where the Name itself is the CVE ID. The result is deduplicated in first-seen
// order; it is empty when no CVE can be extracted.
func extractCVEIDs(v Vulnerability) []string {
	ids := cveRegex.FindAllString(v.Link, -1)
	if len(ids) == 0 {
		ids = cveRegex.FindAllString(v.Name, -1)
	}
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// mapSeverity maps a Quay severity onto the canonical RHEM severity, falling
// back to "Unknown" for unrecognized values so new Clair severities degrade
// gracefully.
func mapSeverity(quaySeverity string) string {
	if s, ok := severityMap[quaySeverity]; ok {
		return s
	}
	return "Unknown"
}

// buildIssuer constructs an Issuer from a Quay NamespaceName. The prefix before
// ":" selects the publisher name (verbatim NamespaceName when unrecognized);
// the ID is a stable UUIDv5 derived from the full NamespaceName. It returns nil
// for an empty NamespaceName.
func buildIssuer(namespaceName string) *vulnerability.Issuer {
	if namespaceName == "" {
		return nil
	}
	prefix, _, _ := strings.Cut(namespaceName, ":")
	name, ok := namespaceToIssuer[prefix]
	if !ok {
		name = namespaceName
	}
	return &vulnerability.Issuer{
		ID:   uuid.NewSHA1(uuid.NameSpaceURL, []byte(namespaceName)).String(),
		Name: name,
	}
}

// cvssScore returns the CVSSv3 score, falling back to CVSSv2, or nil when no NVD
// enrichment is present. A scoring entry counts as present when it carries a
// non-zero score or a vector string, so an absent (zero-value) entry is not
// mistaken for a genuine score of 0.
func cvssScore(m Metadata) *float64 {
	if score, ok := cvssEntryScore(m.NVD.CVSSv3); ok {
		return score
	}
	if score, ok := cvssEntryScore(m.NVD.CVSSv2); ok {
		return score
	}
	return nil
}

func cvssEntryScore(c CVSS) (*float64, bool) {
	if c.Score == 0 && c.Vectors == "" {
		return nil, false
	}
	score := c.Score
	return &score, true
}

// advisoryID returns the vulnerability Name when it identifies an advisory
// (e.g. "RHSA-2024:1234") rather than a CVE, otherwise nil. A Name that is
// itself a CVE (Debian/Alpine style) carries no separate advisory ID.
func advisoryID(name string) *string {
	if name == "" || cveRegex.MatchString(name) {
		return nil
	}
	return &name
}
