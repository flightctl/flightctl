package quay

import (
	"context"
	"encoding/json"
	"fmt"
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

// Skip reasons recorded when an image is not queried or yields no report.
const (
	reasonMissingImageReference   = "missing_image_reference"
	reasonMissingImageDigest      = "missing_image_digest"
	reasonNotOnConfiguredRegistry = "not_on_configured_registry"
	reasonUnparseableReference    = "unparseable_image_reference"
	reasonNotFound                = "not_found"
)

// Client fetches vulnerability reports from a single Quay instance's Security API.
type Client struct {
	endpoint     string
	registryHost string
	token        string
	httpClient   *http.Client
	log          logrus.FieldLogger
}

// NewClient builds a Quay Security API client from the Quay backend config.
// It returns (nil, nil) when cfg is nil, so a caller can treat an absent Quay
// configuration as a disabled backend. It returns an error when the configured
// endpoint has no parseable host. The httpClient parameter allows injection of
// custom TLS config (e.g., custom CA, InsecureSkipVerify); when nil, a default
// client with a 30s timeout is used.
func NewClient(cfg *config.QuayConfig, log logrus.FieldLogger, httpClient *http.Client) (*Client, error) {
	if cfg == nil {
		return nil, nil
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	base, host, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{
		endpoint:     base,
		registryHost: host,
		token:        cfg.Token.Value(),
		httpClient:   httpClient,
		log:          log,
	}, nil
}

// FetchImageSecurity retrieves the Quay Security report for one deployed image.
//
// It returns (report, nil) when the image is hosted on the configured registry
// and Quay reports status "scanned". It returns (nil, nil) when the image is
// skipped for a documented reason (missing image reference, non-matching
// registry, non-"scanned" status, or a non-2xx response such as 404), logging
// the reason at an appropriate level. It returns (nil, error) on a genuine
// failure to build the request, reach Quay, or decode a successful response.
func (c *Client) FetchImageSecurity(ctx context.Context, image vulnerability.ImageRef) (*Response, error) {
	if image.Image == "" {
		c.log.WithFields(logrus.Fields{"digest": image.Digest, "reason": reasonMissingImageReference}).
			Debug("skipping image")
		return nil, nil
	}

	if image.Digest == "" {
		c.log.WithFields(logrus.Fields{"image": image.Image, "reason": reasonMissingImageDigest}).
			Debug("skipping image")
		return nil, nil
	}

	host, repoPath, err := parseImageReference(image.Image)
	if err != nil {
		c.log.WithFields(logrus.Fields{"image": image.Image, "reason": reasonUnparseableReference}).
			WithError(err).Debug("skipping image")
		return nil, nil
	}

	if host != c.registryHost {
		c.log.WithFields(logrus.Fields{"digest": image.Digest, "host": host, "reason": reasonNotOnConfiguredRegistry}).
			Debug("skipping image")
		return nil, nil
	}

	reqURL := c.securityURL(repoPath, image.Digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building quay security request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying quay security api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			c.log.WithFields(logrus.Fields{"digest": image.Digest, "reason": reasonNotFound}).
				Debug("skipping image: not found on configured registry")
			return nil, nil
		}
		c.logNonOK(image.Digest, resp.StatusCode)
		return nil, fmt.Errorf("quay security api returned %d for image %s", resp.StatusCode, image.Digest)
	}

	var report Response
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return nil, fmt.Errorf("decoding quay security response: %w", err)
	}

	if report.Status != statusScanned {
		// Dynamic reason construction handles any future statuses Quay might add
		// beyond the known set (queued, pending, unsupported, failed).
		c.log.WithFields(logrus.Fields{
			"digest": image.Digest,
			"status": report.Status,
			"reason": "scan_" + report.Status,
		}).Info("skipping image: scan not complete")
		return nil, nil
	}

	// Quay's contract: status="scanned" always has non-nil Data.Layer (even if
	// Features is empty for zero vulnerabilities). Treat nil as malformed.
	if report.Data == nil || report.Data.Layer == nil {
		c.log.WithFields(logrus.Fields{
			"digest": image.Digest,
			"status": report.Status,
			"reason": "malformed_scanned_response",
		}).Warn("skipping image: scanned status with nil data")
		return nil, nil
	}

	return &report, nil
}

// logNonOK logs a non-200 Quay response at a level appropriate to the cause.
func (c *Client) logNonOK(digest string, statusCode int) {
	entry := c.log.WithFields(logrus.Fields{"digest": digest, "status_code": statusCode})
	switch statusCode {
	case http.StatusNotFound:
		entry.WithField("reason", reasonNotFound).Debug("skipping image: not found on configured registry")
	case http.StatusUnauthorized:
		entry.WithField("endpoint", c.endpoint).Error("quay authentication failed")
	case http.StatusForbidden:
		entry.Warn("quay authorization failed: insufficient permissions")
	default:
		entry.Warn("unexpected quay security api response")
	}
}

// securityURL builds the Quay Security API URL for a repository path and digest.
func (c *Client) securityURL(repoPath, digest string) string {
	return fmt.Sprintf("%s/api/v1/repository/%s/manifest/%s/security?vulnerabilities=true",
		c.endpoint, repoPath, digest)
}

// parseEndpoint normalizes a configured endpoint URL into a base URL (used to
// build request URLs) and its normalized hostname (used to filter images by
// registry). The hostname is lowercased and default ports (443 for https, 80
// for http) are stripped to match the normalization applied by reference.Domain
// on image references. A scheme is assumed to be HTTPS when omitted.
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
	// url.URL already splits host and port correctly (including IPv6 literals).
	host = normalizeHost(u.Hostname(), u.Port(), u.Scheme)
	return base, host, nil
}

// normalizeHost normalizes a registry hostname by lowercasing it and stripping
// default ports (443 for https, 80 for http). hostname and port are the already
// split host components (port may be empty); the result is rebuilt with
// net.JoinHostPort so IPv6 literals are bracketed correctly. This matches the
// normalization behavior of reference.Domain for image references.
func normalizeHost(hostname, port, scheme string) string {
	hostname = strings.ToLower(hostname)
	if port == "" {
		return hostname
	}
	// Strip default ports.
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return hostname
	}
	return net.JoinHostPort(hostname, port)
}

// splitHostPort splits a "host" or "host:port" string using net.SplitHostPort,
// falling back to treating the whole string as the host when no port is present.
// Unlike a manual LastIndex(":") split, this handles IPv6 literals correctly.
func splitHostPort(host string) (hostname, port string) {
	if h, p, err := net.SplitHostPort(host); err == nil {
		return h, p
	}
	return host, ""
}

// parseImageReference normalizes an image reference into its registry host and
// repository path ("namespace/repo"), stripping any scheme prefix. The host is
// normalized (lowercased, default HTTPS port 443 stripped) to match parseEndpoint.
func parseImageReference(imageRef string) (host, repoPath string, err error) {
	ref := imageRef
	if idx := strings.Index(ref, "://"); idx != -1 {
		ref = ref[idx+3:]
	}
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", "", err
	}
	// Docker registry references default to HTTPS, so normalize with https scheme.
	hostname, port := splitHostPort(reference.Domain(named))
	return normalizeHost(hostname, port, "https"), reference.Path(named), nil
}
