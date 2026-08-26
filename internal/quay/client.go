package quay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/vulnerability"
	"github.com/sirupsen/logrus"
)

const defaultHTTPTimeout = 30 * time.Second

// statusScanned is the only Quay scan status that carries vulnerability data.
const statusScanned = "scanned"

// Retry policy for transient Quay Security API failures. HTTP 429, HTTP 5xx,
// and request timeouts are retried up to maxRetries total attempts; the delay
// starts at initialBackoff, doubles after each attempt, is capped at
// maxBackoff, and carries additive jitter of up to half the current delay.
// Non-transient failures (4xx other than 429, connection errors, decode
// errors) are not retried.
const (
	maxRetries     = 3
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
)

// Skip reasons recorded when an image is not queried or yields no report.
const (
	reasonMissingImageReference   = "missing_image_reference"
	reasonMissingImageDigest      = "missing_image_digest"
	reasonNotOnConfiguredRegistry = "not_on_configured_registry"
	reasonUnparseableReference    = "unparseable_image_reference"
	reasonNotFound                = "not_found"
	reasonForbidden               = "forbidden"
)

// Structured log event names emitted by the Quay backend (design §6). They are
// carried in the "event" log field so operators can filter by outcome.
const (
	eventScanCompleted = "quay_scan_completed"
	eventScanSkipped   = "quay_scan_skipped"
	eventScanFailed    = "quay_scan_failed"
	eventRateLimited   = "quay_rate_limited"
	eventAuthError     = "quay_auth_error"
	eventSyncSummary   = "quay_sync_summary"
)

// ErrQuayAuth is returned when Quay rejects the configured token (HTTP 401). It
// is terminal: the scanner aborts the whole scan rather than skipping the image,
// because every subsequent request would fail the same way.
var ErrQuayAuth = errors.New("quay authentication failed: verify OAuth2 Application Token")

// fetchOutcome classifies how one image's security fetch resolved, so the
// scanner can aggregate summary counts. It is meaningful only when the fetch
// returned no error.
type fetchOutcome int

const (
	outcomeScanned         fetchOutcome = iota // Report is present and status "scanned"
	outcomeSkippedRegistry                     // missing/other-registry/unparseable reference, missing digest
	outcomeSkipped                             // 404, 403, or a non-"scanned" scan status
)

// fetchResult is the outcome of fetching one image's Quay Security report.
// Report is non-nil only when Outcome is outcomeScanned. Attempts is the number
// of HTTP attempts made (Attempts-1 is the number of retries), including when
// the fetch ultimately fails.
type fetchResult struct {
	Report   *Response
	Outcome  fetchOutcome
	Attempts int
}

// Client fetches vulnerability reports from a single Quay instance's Security API.
type Client struct {
	endpoint     string
	registryHost string
	token        string
	httpClient   *http.Client
	backoffBase  time.Duration
	log          logrus.FieldLogger
}

// NewClient builds a Quay Security API client from the Quay backend config.
// It returns (nil, nil) when cfg is nil, so a caller can treat an absent Quay
// configuration as a disabled backend. It returns an error when the configured
// endpoint has no parseable host.
func NewClient(cfg *config.QuayConfig, log logrus.FieldLogger) (*Client, error) {
	if cfg == nil {
		return nil, nil
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	base, host, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{
		endpoint:     base,
		registryHost: host,
		token:        cfg.Token.Value(),
		httpClient:   &http.Client{Timeout: defaultHTTPTimeout},
		backoffBase:  initialBackoff,
		log:          log,
	}, nil
}

// FetchImageSecurity retrieves the Quay Security report for one deployed image.
//
// It returns a scanned report when the image is hosted on the configured
// registry and Quay reports status "scanned". It returns a no-report result
// (with a classified Outcome) when the image is skipped for a documented reason
// (missing reference, non-matching registry, non-"scanned" status, 404, 403).
// It returns ErrQuayAuth on HTTP 401 and a wrapped error on any genuine failure
// to reach Quay (after retries) or decode a successful response.
func (c *Client) FetchImageSecurity(ctx context.Context, image vulnerability.ImageRef) (fetchResult, error) {
	if image.Image == "" {
		c.logSkip(logrus.Fields{"digest": image.Digest, "reason": reasonMissingImageReference})
		return fetchResult{Outcome: outcomeSkippedRegistry}, nil
	}
	if image.Digest == "" {
		c.logSkip(logrus.Fields{"image": image.Image, "reason": reasonMissingImageDigest})
		return fetchResult{Outcome: outcomeSkippedRegistry}, nil
	}

	host, repoPath, err := parseImageReference(image.Image)
	if err != nil {
		c.log.WithFields(logrus.Fields{"event": eventScanSkipped, "image": image.Image, "reason": reasonUnparseableReference}).
			WithError(err).Debug("skipping image")
		return fetchResult{Outcome: outcomeSkippedRegistry}, nil
	}

	if host != c.registryHost {
		c.logSkip(logrus.Fields{"digest": image.Digest, "host": host, "reason": reasonNotOnConfiguredRegistry})
		return fetchResult{Outcome: outcomeSkippedRegistry}, nil
	}

	reqURL := c.securityURL(repoPath, image.Digest)
	resp, attempts, err := c.retryableGet(ctx, reqURL, image.Digest)
	if err != nil {
		return fetchResult{Outcome: outcomeSkipped, Attempts: attempts}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if err := c.classifyNonOK(image.Digest, resp.StatusCode); err != nil {
			return fetchResult{Outcome: outcomeSkipped, Attempts: attempts}, err
		}
		return fetchResult{Outcome: outcomeSkipped, Attempts: attempts}, nil
	}

	var report Response
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return fetchResult{Outcome: outcomeSkipped, Attempts: attempts}, fmt.Errorf("decoding quay security response: %w", err)
	}

	if report.Status != statusScanned {
		// Dynamic reason construction handles any future statuses Quay might add
		// beyond the known set (queued, pending, unsupported, failed).
		c.log.WithFields(logrus.Fields{
			"event":  eventScanSkipped,
			"digest": image.Digest,
			"status": report.Status,
			"reason": "scan_" + report.Status,
		}).Info("skipping image: scan not complete")
		return fetchResult{Outcome: outcomeSkipped, Attempts: attempts}, nil
	}

	return fetchResult{Report: &report, Outcome: outcomeScanned, Attempts: attempts}, nil
}

// retryableGet issues an authenticated GET and retries transient failures (HTTP
// 429, 5xx, and request timeouts) per the package retry policy, honoring ctx
// cancellation between attempts. It returns the successful response, the number
// of attempts made, and any terminal error. On a 429 it emits a
// quay_rate_limited event. Non-timeout transport errors are returned
// immediately without retry.
func (c *Client) retryableGet(ctx context.Context, reqURL, digest string) (*http.Response, int, error) {
	backoff := c.backoffBase
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := c.doGet(ctx, reqURL)
		switch {
		case err != nil && isTimeout(err):
			lastErr = fmt.Errorf("quay security api request timed out: %w", err)
		case err != nil:
			return nil, attempt, fmt.Errorf("querying quay security api: %w", err)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
			resp.Body.Close()
			lastErr = fmt.Errorf("quay security api returned status %d", resp.StatusCode)
		default:
			return resp, attempt, nil
		}

		if attempt == maxRetries {
			break
		}

		wait := backoffWithJitter(backoff)
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			c.log.WithFields(logrus.Fields{
				"event":          eventRateLimited,
				"digest":         digest,
				"attempt":        attempt,
				"retry_after_ms": wait.Milliseconds(),
			}).Warn("quay rate limited; backing off")
		}
		select {
		case <-ctx.Done():
			return nil, attempt, ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
	return nil, maxRetries, fmt.Errorf("max retries exceeded querying quay security api: %w", lastErr)
}

// doGet builds and sends a single authenticated GET request.
func (c *Client) doGet(ctx context.Context, reqURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building quay security request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	return c.httpClient.Do(req)
}

// classifyNonOK handles a non-retryable, non-200 Quay response. It returns
// ErrQuayAuth for 401 (terminal) and nil for statuses that skip the image (404,
// 403, and any other unexpected status), logging each at an appropriate level.
func (c *Client) classifyNonOK(digest string, statusCode int) error {
	entry := c.log.WithFields(logrus.Fields{"digest": digest, "status_code": statusCode})
	switch statusCode {
	case http.StatusNotFound:
		entry.WithFields(logrus.Fields{"event": eventScanSkipped, "reason": reasonNotFound}).
			Debug("skipping image: not found on configured registry")
	case http.StatusUnauthorized:
		c.log.WithFields(logrus.Fields{"event": eventAuthError, "endpoint": c.endpoint, "status_code": statusCode}).
			Error("quay authentication failed")
		return ErrQuayAuth
	case http.StatusForbidden:
		entry.WithFields(logrus.Fields{"event": eventScanSkipped, "reason": reasonForbidden}).
			Warn("quay authorization failed: insufficient permissions")
	default:
		entry.WithField("event", eventScanSkipped).Warn("unexpected quay security api response")
	}
	return nil
}

// logSkip emits a quay_scan_skipped event at debug level.
func (c *Client) logSkip(fields logrus.Fields) {
	fields["event"] = eventScanSkipped
	c.log.WithFields(fields).Debug("skipping image")
}

// securityURL builds the Quay Security API URL for a repository path and digest.
func (c *Client) securityURL(repoPath, digest string) string {
	return fmt.Sprintf("%s/api/v1/repository/%s/manifest/%s/security?vulnerabilities=true",
		c.endpoint, repoPath, digest)
}

// backoffWithJitter returns the backoff extended by additive jitter of up to
// half the backoff, spreading retries so concurrent callers do not synchronize.
func backoffWithJitter(backoff time.Duration) time.Duration {
	half := int64(backoff / 2)
	if half <= 0 {
		return backoff
	}
	return backoff + time.Duration(rand.Int64N(half))
}

// isTimeout reports whether err is a request timeout (as opposed to another
// transport error such as a refused connection).
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// parseEndpoint normalizes a configured endpoint URL into a base URL (used to
// build request URLs) and its "host[:port]" (used to filter images by
// registry). A scheme is assumed to be HTTPS when omitted, so both values stay
// consistent regardless of how the endpoint was written.
func parseEndpoint(endpoint string) (base, host string, err error) {
	e := strings.TrimSpace(endpoint)
	if e == "" {
		return "", "", fmt.Errorf("quay endpoint is empty")
	}
	if !strings.Contains(e, "://") {
		e = "https://" + e
	}
	u, err := url.Parse(e)
	if err != nil {
		return "", "", fmt.Errorf("parsing quay endpoint %q: %w", endpoint, err)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("quay endpoint %q has no host", endpoint)
	}
	base = strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/")
	return base, u.Host, nil
}

// parseImageReference normalizes an image reference into its registry host and
// repository path ("namespace/repo"), stripping any scheme prefix.
func parseImageReference(imageRef string) (host, repoPath string, err error) {
	ref := imageRef
	if idx := strings.Index(ref, "://"); idx != -1 {
		ref = ref[idx+3:]
	}
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", "", err
	}
	return reference.Domain(named), reference.Path(named), nil
}
