package trustifyv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	AuthModeNone              = "none"
	AuthModeClientCredentials = "client-credentials"

	defaultHTTPTimeout = 30 * time.Second

	// maxDigestsPerQuery limits how many digests we include in a single SBOM query
	// to avoid URL length limits. 30 digests * ~70 chars each ≈ 2100 chars.
	maxDigestsPerQuery = 30
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
	// GetVulnerabilitiesForDigests queries Trustify for all CVE findings associated
	// with the given image digests. It returns a map from image digest to findings.
	// Digests not found in Trustify will have an empty slice in the map.
	GetVulnerabilitiesForDigests(ctx context.Context, digests []string) (map[string][]Finding, error)

	// UploadSBOM uploads an SBOM document to Trustify, associating it with the
	// given image digest. The SBOM should be in CycloneDX or SPDX JSON format.
	UploadSBOM(ctx context.Context, sbomData []byte, imageDigest string) error
}

type vulnerabilityClient struct {
	endpoint string
	api      ClientWithResponsesInterface
}

// NewVulnerabilityClient creates a new Trustify v2 client from the provided
// configuration. Returns an AuthError if client-credentials mode is selected
// but OIDC discovery fails. Returns nil, nil when cfg is nil (feature disabled).
func NewVulnerabilityClient(ctx context.Context, cfg *config.TrustifyConfig) (VulnerabilityClient, error) {
	if cfg == nil {
		return nil, nil
	}

	httpClient, err := buildHTTPClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	apiClient, err := NewClientWithResponses(cfg.Endpoint, WithHTTPClient(httpClient))
	if err != nil {
		return nil, &ConnectionError{Endpoint: cfg.Endpoint, Err: fmt.Errorf("creating API client: %w", err)}
	}

	return &vulnerabilityClient{
		endpoint: cfg.Endpoint,
		api:      apiClient,
	}, nil
}

// GetVulnerabilitiesForDigests queries Trustify for CVE findings for the given
// image digests using the 3-step PURL-based approach:
// 1. Query PURLs by digest (batched) - finds OCI PURLs containing the image digest
// 2. For each PURL found, query SBOMs containing that package
// 3. For each SBOM, fetch its advisories
func (c *vulnerabilityClient) GetVulnerabilitiesForDigests(ctx context.Context, digests []string) (map[string][]Finding, error) {
	results := make(map[string][]Finding, len(digests))
	for _, d := range digests {
		results[d] = nil
	}

	if len(digests) == 0 {
		return results, nil
	}

	// Step 1: Find PURLs for all digests (batched)
	purlsByDigest, err := c.findPURLsForDigests(ctx, digests)
	if err != nil {
		return nil, err
	}

	// Step 2: Find SBOMs for each PURL
	sbomsByDigest := make(map[string]*SbomSummary, len(digests))
	for digest, purl := range purlsByDigest {
		if purl == "" {
			continue
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		sbom, err := c.findSBOMByPURL(ctx, purl)
		if err != nil {
			return nil, fmt.Errorf("finding SBOM for digest %s: %w", digest, err)
		}
		if sbom != nil {
			sbomsByDigest[digest] = sbom
		}
	}

	// Step 3: Fetch advisories for each unique SBOM
	uniqueSBOMs := make(map[string]string) // sbomID -> digest
	for digest, sbom := range sbomsByDigest {
		if sbom != nil {
			uniqueSBOMs[sbom.Id] = digest
		}
	}

	for sbomID, digest := range uniqueSBOMs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		findings, err := c.getAdvisoriesForSBOM(ctx, sbomID, digest)
		if err != nil {
			return nil, fmt.Errorf("fetching advisories for SBOM %s: %w", sbomID, err)
		}

		results[digest] = findings
	}

	return results, nil
}

// UploadSBOM uploads an SBOM document to Trustify.
func (c *vulnerabilityClient) UploadSBOM(ctx context.Context, sbomData []byte, imageDigest string) error {
	// Build labels query parameter with the image digest
	// Format: sha256~<digest> to match how Trustify indexes SBOMs
	labels := "sha256~" + stripSHA256Prefix(imageDigest)

	resp, err := c.api.UploadSbomWithBodyWithResponse(ctx, &UploadSbomParams{
		Labels: &labels,
	}, "application/json", strings.NewReader(string(sbomData)))
	if err != nil {
		return &ConnectionError{Endpoint: c.endpoint, Err: fmt.Errorf("uploading SBOM: %w", err)}
	}

	switch resp.StatusCode() {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	case http.StatusConflict:
		// SBOM already exists, which is fine
		return nil
	case http.StatusBadRequest:
		return fmt.Errorf("invalid SBOM format: %s", string(resp.Body))
	default:
		return &ConnectionError{
			Endpoint: c.endpoint,
			Err:      fmt.Errorf("unexpected status %d uploading SBOM: %s", resp.StatusCode(), resp.Body),
		}
	}
}

// findPURLsForDigests queries Trustify for OCI PURLs matching the given image digests.
// Returns a map from digest to full PURL string (empty string if not found).
func (c *vulnerabilityClient) findPURLsForDigests(ctx context.Context, digests []string) (map[string]string, error) {
	result := make(map[string]string, len(digests))

	// Process in batches to avoid URL length limits
	for i := 0; i < len(digests); i += maxDigestsPerQuery {
		end := i + maxDigestsPerQuery
		if end > len(digests) {
			end = len(digests)
		}
		batch := digests[i:end]

		purls, err := c.queryPURLsByDigests(ctx, batch)
		if err != nil {
			return nil, err
		}

		// Match returned PURLs to requested digests
		for _, purlItem := range purls {
			for _, d := range batch {
				hexDigest := stripSHA256Prefix(d)
				if strings.Contains(purlItem.Purl, hexDigest) {
					result[d] = purlItem.Purl
					break
				}
			}
		}
	}

	return result, nil
}

// queryPURLsByDigests queries Trustify for PURLs containing any of the given digests.
func (c *vulnerabilityClient) queryPURLsByDigests(ctx context.Context, digests []string) ([]PurlSummary, error) {
	// Build query: digest1|digest2|digest3
	// Use raw hex digests for text search (no sha256: prefix)
	var queryParts []string
	for _, d := range digests {
		queryParts = append(queryParts, stripSHA256Prefix(d))
	}
	query := strings.Join(queryParts, "|")

	resp, err := c.api.ListPurlsWithResponse(ctx, &ListPurlsParams{Q: &query})
	if err != nil {
		return nil, &ConnectionError{Endpoint: c.endpoint, Err: fmt.Errorf("listing PURLs: %w", err)}
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, &ConnectionError{
			Endpoint: c.endpoint,
			Err:      fmt.Errorf("unexpected status %d listing PURLs: %s", resp.StatusCode(), resp.Body),
		}
	}

	if resp.JSON200 == nil {
		return nil, nil
	}

	return resp.JSON200.Items, nil
}

// findSBOMByPURL queries Trustify for SBOMs containing the given PURL.
func (c *vulnerabilityClient) findSBOMByPURL(ctx context.Context, purl string) (*SbomSummary, error) {
	resp, err := c.api.ListSbomsByPackageWithResponse(ctx, &ListSbomsByPackageParams{Purl: purl})
	if err != nil {
		return nil, &ConnectionError{Endpoint: c.endpoint, Err: fmt.Errorf("finding SBOM by PURL: %w", err)}
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, &ConnectionError{
			Endpoint: c.endpoint,
			Err:      fmt.Errorf("unexpected status %d finding SBOM by PURL: %s", resp.StatusCode(), resp.Body),
		}
	}

	if resp.JSON200 == nil || len(resp.JSON200.Items) == 0 {
		return nil, nil
	}

	// Return the first SBOM found (there should typically be only one for an OCI PURL)
	return &resp.JSON200.Items[0], nil
}

// getAdvisoriesForSBOM fetches all advisories for an SBOM and converts them to Findings.
func (c *vulnerabilityClient) getAdvisoriesForSBOM(ctx context.Context, sbomID, imageDigest string) ([]Finding, error) {
	resp, err := c.api.GetSbomAdvisoriesWithResponse(ctx, sbomID)
	if err != nil {
		return nil, &ConnectionError{Endpoint: c.endpoint, Err: fmt.Errorf("fetching advisories: %w", err)}
	}

	if resp.StatusCode() == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, &ConnectionError{
			Endpoint: c.endpoint,
			Err:      fmt.Errorf("unexpected status %d fetching advisories: %s", resp.StatusCode(), resp.Body),
		}
	}

	if resp.JSON200 == nil {
		return nil, nil
	}

	return parseAdvisoriesToFindings(imageDigest, *resp.JSON200), nil
}

// parseAdvisoriesToFindings converts Trustify advisories to Finding records.
func parseAdvisoriesToFindings(imageDigest string, advisories []SbomAdvisory) []Finding {
	var findings []Finding

	for i := range advisories {
		adv := &advisories[i]

		// Each advisory can have multiple status entries (one per CVE)
		for j := range adv.Status {
			st := &adv.Status[j]

			finding := Finding{
				ImageDigest: imageDigest,
				CVEID:       st.Identifier, // CVE ID is in the status entry, not the advisory
				Status:      st.Status,
				Severity:    string(st.AverageSeverity),
				AdvisoryID:  adv.Identifier,
				PublishedAt: adv.Published,
			}

			if adv.Issuer != nil {
				finding.Issuer = adv.Issuer
			}

			score := st.AverageScore
			finding.CVSSScore = &score

			if st.Description != nil {
				finding.Description = *st.Description
			} else if st.Title != nil {
				finding.Description = *st.Title
			} else if adv.Title != nil {
				finding.Description = *adv.Title
			}

			findings = append(findings, finding)
		}
	}

	return findings
}

// stripSHA256Prefix removes the "sha256:" prefix if present.
func stripSHA256Prefix(s string) string {
	return strings.TrimPrefix(s, "sha256:")
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
	if err := decodeJSON(resp.Body, &doc); err != nil {
		return "", fmt.Errorf("decoding discovery document: %w", err)
	}
	if doc.TokenEndpoint == "" {
		return "", fmt.Errorf("discovery document does not contain token_endpoint")
	}

	return doc.TokenEndpoint, nil
}

// decodeJSON is a helper to decode JSON from a reader.
func decodeJSON(r interface{ Read([]byte) (int, error) }, v interface{}) error {
	return json.NewDecoder(r).Decode(v)
}
