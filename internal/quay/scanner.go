package quay

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

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
	client        *Client
	log           logrus.FieldLogger
	maxConcurrent int
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
	maxConcurrent := cfg.MaxConcurrentRequests
	if maxConcurrent <= 0 {
		maxConcurrent = config.DefaultQuayMaxConcurrentRequests
	}
	return &quayScanner{client: client, log: log, maxConcurrent: maxConcurrent}, nil
}

// syncCounts accumulates the per-image outcomes of one scan cycle for the
// quay_sync_summary event. Every image lands in exactly one of scanned,
// skippedRegistry, skippedError, or failed; retried is the total number of
// retries (extra HTTP attempts) made across all images.
type syncCounts struct {
	scanned         int
	skippedRegistry int
	skippedError    int
	failed          int
	retried         int
}

// ScanImages fetches each image's Quay Security report concurrently and converts
// the contained vulnerabilities into findings keyed by image digest. Requests
// are bounded by a semaphore sized to the configured MaxConcurrentRequests.
//
// Failures are isolated per image: an image the client skips (missing reference,
// other registry, non-"scanned" status, 404, 403) contributes no findings, and a
// genuine fetch or decode error for one image is logged (quay_scan_failed) and
// skipped so the remaining images still scan. A top-level error is returned only
// when every image fails. HTTP 401 is terminal: the first occurrence cancels the
// in-flight scan and ScanImages returns ErrQuayAuth without a partial map. A
// quay_sync_summary event with the aggregate counts is emitted after a
// non-aborted cycle.
func (s *quayScanner) ScanImages(ctx context.Context, images []vulnerability.ImageRef) (map[string][]vulnerability.Finding, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		out     = make(map[string][]vulnerability.Finding)
		counts  syncCounts
		authErr error
	)
	sem := make(chan struct{}, s.maxConcurrent)

	for _, image := range images {
		wg.Add(1)
		go func(image vulnerability.ImageRef) {
			defer wg.Done()

			// Acquire a slot, bailing out immediately if the scan was cancelled
			// (e.g. a sibling image hit a 401) before we ever issued a request.
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			start := time.Now()
			res, err := s.client.FetchImageSecurity(ctx, image)

			mu.Lock()
			defer mu.Unlock()
			if res.Attempts > 1 {
				counts.retried += res.Attempts - 1
			}

			if err != nil {
				if errors.Is(err, ErrQuayAuth) {
					if authErr == nil {
						authErr = err
					}
					cancel() // fail-fast: stop siblings still queued on the semaphore
					return
				}
				if ctx.Err() != nil {
					// The scan is being torn down (a sibling's 401 fail-fast or
					// parent cancellation); this error is induced, not a genuine
					// per-image failure, so it is neither counted nor logged.
					return
				}
				counts.failed++
				s.log.WithFields(logrus.Fields{
					"event":    eventScanFailed,
					"digest":   image.Digest,
					"attempts": res.Attempts,
				}).WithError(err).Warn("quay image scan failed; skipping")
				return
			}

			switch res.Outcome {
			case outcomeScanned:
				findings := findingsFromReport(image.Digest, res.Report, s.log)
				counts.scanned++
				s.log.WithFields(logrus.Fields{
					"event":       eventScanCompleted,
					"digest":      image.Digest,
					"cve_count":   len(findings),
					"duration_ms": time.Since(start).Milliseconds(),
				}).Info("quay image scan completed")
				if len(findings) > 0 {
					out[image.Digest] = append(out[image.Digest], findings...)
				}
			case outcomeSkippedRegistry:
				counts.skippedRegistry++
			case outcomeSkippedError:
				counts.skippedError++
			}
		}(image)
	}
	wg.Wait()

	if authErr != nil {
		return nil, authErr
	}
	// Respect parent-context cancellation (shutdown/deadline): surface it rather
	// than returning a partial map the caller would mistake for a full scan.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.log.WithFields(logrus.Fields{
		"event":            eventSyncSummary,
		"total_images":     len(images),
		"scanned":          counts.scanned,
		"skipped_registry": counts.skippedRegistry,
		"skipped_error":    counts.skippedError,
		"failed":           counts.failed,
		"retried":          counts.retried,
	}).Info("quay vulnerability scan complete")

	if counts.failed > 0 && counts.failed == len(images) {
		return nil, fmt.Errorf("all %d image scans failed", len(images))
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
