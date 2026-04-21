package trustifyv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	AuthModeNone              = "none"
	AuthModeClientCredentials = "client-credentials"

	defaultHTTPTimeout = 30 * time.Second
)

// AuthError is returned when authentication against Trustify fails.
type AuthError struct {
	Mode string
	Err  error
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("trustify authentication error (mode=%s): %v", e.Mode, e.Err)
}

func (e *AuthError) Unwrap() error { return e.Err }

// ConnectionError is returned when the HTTP request to Trustify fails.
type ConnectionError struct {
	Endpoint string
	Err      error
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("trustify connection error (endpoint=%s): %v", e.Endpoint, e.Err)
}

func (e *ConnectionError) Unwrap() error { return e.Err }

// VulnerabilityClient queries the Trustify v2 API for vulnerability findings.
type VulnerabilityClient interface {
	GetVulnerabilities(ctx context.Context, imageDigest string) ([]Finding, error)
}

type vulnerabilityClient struct {
	endpoint string
	// ClientWithResponsesInterface is defined in client.gen.go (same package).
	api ClientWithResponsesInterface
}

// NewVulnerabilityClient creates a new Trustify v2 client from the provided
// configuration.  Returns an AuthError if client-credentials mode is selected
// but OIDC discovery fails.  Returns nil, nil when cfg is nil (feature disabled).
func NewVulnerabilityClient(ctx context.Context, cfg *config.TrustifyConfig) (VulnerabilityClient, error) {
	if cfg == nil {
		return nil, nil
	}

	httpClient, err := buildHTTPClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// NewClientWithResponses and WithHTTPClient are defined in client.gen.go.
	apiClient, err := NewClientWithResponses(cfg.Endpoint, WithHTTPClient(httpClient))
	if err != nil {
		return nil, &ConnectionError{Endpoint: cfg.Endpoint, Err: fmt.Errorf("creating API client: %w", err)}
	}

	return &vulnerabilityClient{
		endpoint: cfg.Endpoint,
		api:      apiClient,
	}, nil
}

// GetVulnerabilities queries the Trustify /api/v2/vulnerability/analyze endpoint
// for all CVE findings associated with the given image digest and returns them
// as a slice of Finding.
//
// The image digest is wrapped as an OCI PURL (pkg:oci/unknown@<digest>) before
// being sent to the analyze endpoint.
func (c *vulnerabilityClient) GetVulnerabilities(ctx context.Context, imageDigest string) ([]Finding, error) {
	purl := ociPURL(imageDigest)

	resp, err := c.api.AnalyzeWithResponse(ctx, AnalysisRequest{
		Purls: []string{purl},
	})
	if err != nil {
		return nil, &ConnectionError{Endpoint: c.endpoint, Err: fmt.Errorf("executing request: %w", err)}
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, &ConnectionError{
			Endpoint: c.endpoint,
			Err:      fmt.Errorf("unexpected status %d: %s", resp.StatusCode(), resp.Body),
		}
	}

	if resp.JSON200 == nil {
		return nil, nil
	}

	apiResp := *resp.JSON200
	result, ok := apiResp[purl]
	if !ok {
		return nil, nil
	}

	return parseFindings(imageDigest, result), nil
}

// ociPURL wraps an image digest in the pkg:oci PURL scheme expected by the
// Trustify analyze endpoint.
func ociPURL(imageDigest string) string {
	return "pkg:oci/unknown@" + imageDigest
}

// buildHTTPClient constructs an http.Client according to the auth mode.
func buildHTTPClient(ctx context.Context, cfg *config.TrustifyConfig) (*http.Client, error) {
	if cfg.Auth == nil || cfg.Auth.Mode == AuthModeNone || cfg.Auth.Mode == "" {
		return &http.Client{Timeout: defaultHTTPTimeout}, nil
	}

	if cfg.Auth.Mode != AuthModeClientCredentials {
		return nil, &AuthError{
			Mode: cfg.Auth.Mode,
			Err:  fmt.Errorf("unsupported authentication mode %q", cfg.Auth.Mode),
		}
	}

	tokenURL, err := discoverTokenEndpoint(ctx, cfg.Auth.OIDCIssuerURL)
	if err != nil {
		return nil, &AuthError{Mode: cfg.Auth.Mode, Err: fmt.Errorf("OIDC discovery: %w", err)}
	}

	ccCfg := &clientcredentials.Config{
		ClientID:     cfg.Auth.ClientID,
		ClientSecret: string(cfg.Auth.ClientSecret),
		TokenURL:     tokenURL,
	}

	// oauth2 transports handle token acquisition and caching automatically.
	oauthClient := ccCfg.Client(ctx)
	oauthClient.Timeout = defaultHTTPTimeout
	return oauthClient, nil
}

// discoverTokenEndpoint fetches the OIDC discovery document and returns the
// token_endpoint value.
func discoverTokenEndpoint(ctx context.Context, issuerURL string) (string, error) {
	if issuerURL == "" {
		return "", fmt.Errorf("oidcIssuerUrl must not be empty for client-credentials mode")
	}

	base, err := url.Parse(issuerURL)
	if err != nil {
		return "", fmt.Errorf("invalid oidcIssuerUrl %q: %w", issuerURL, err)
	}
	discoveryURL := base.JoinPath(".well-known/openid-configuration").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpClient := &http.Client{Timeout: defaultHTTPTimeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching discovery document from %s: %w", discoveryURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
	}

	var doc struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("decoding discovery document: %w", err)
	}
	if doc.TokenEndpoint == "" {
		return "", fmt.Errorf("discovery document does not contain token_endpoint")
	}

	return doc.TokenEndpoint, nil
}

// parseFindings converts an AnalysisResult into Finding records for the given
// image digest.
//
// AnalysisResult.Details has one AnalysisDetails per CVE.  Each detail's
// Status map keys are status strings (e.g. "affected"); the values are the
// advisories asserting that status.  We emit one Finding per (CVE × status ×
// advisory) tuple, picking the highest-scoring Score for CVSSScore/Severity.
func parseFindings(imageDigest string, result AnalysisResult) []Finding {
	var findings []Finding

	for i := range result.Details {
		detail := &result.Details[i]

		desc := ""
		if detail.Description != nil {
			desc = *detail.Description
		}

		for status, advisories := range detail.Status {
			for j := range advisories {
				adv := &advisories[j]

				finding := Finding{
					ImageDigest: imageDigest,
					CVEID:       detail.Identifier,
					Status:      status,
					AdvisoryID:  adv.Identifier,
					Description: desc,
					PublishedAt: adv.Published,
				}

				if best := bestScore(adv.Scores); best != nil {
					v := best.Value
					finding.CVSSScore = &v
					finding.Severity = string(best.Severity)
				}

				findings = append(findings, finding)
			}
		}
	}

	return findings
}

// bestScore returns the Score entry with the highest value, or nil when the
// slice is empty.
func bestScore(scores []Score) *Score {
	if len(scores) == 0 {
		return nil
	}
	best := &scores[0]
	for i := 1; i < len(scores); i++ {
		if scores[i].Value > best.Value {
			best = &scores[i]
		}
	}
	return best
}
