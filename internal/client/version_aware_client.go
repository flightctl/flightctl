package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/pkg/version"
)

// VersionAwareClient wraps the API client with version compatibility checking
type VersionAwareClient struct {
	*apiclient.ClientWithResponses
	versionChecker  *version.VersionCompatibilityChecker
	versionChecked  bool
	versionCheckErr error
	mu              sync.Mutex
}

// NewVersionAwareClient creates a new version-aware client
func NewVersionAwareClient(client *apiclient.ClientWithResponses) *VersionAwareClient {
	return &VersionAwareClient{
		ClientWithResponses: client,
		versionChecker:      version.NewVersionCompatibilityChecker(),
		versionChecked:      false,
	}
}

// CheckVersionCompatibility checks if the client version is compatible with the server version
func (c *VersionAwareClient) CheckVersionCompatibility(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.versionChecked {
		return c.versionCheckErr
	}

	response, err := c.GetVersionWithResponse(ctx)
	if err != nil {
		c.versionChecked = true
		c.versionCheckErr = nil
		return nil
	}

	serverVersion, err := c.processVersionResponse(response)
	if err != nil {
		c.versionChecked = true
		c.versionCheckErr = nil
		return nil
	}

	c.versionCheckErr = c.versionChecker.CheckCompatibility(serverVersion)
	c.versionChecked = true
	return c.versionCheckErr
}

// processVersionResponse processes the version API response
func (c *VersionAwareClient) processVersionResponse(response *apiclient.GetVersionResponse) (*api.Version, error) {
	if response == nil {
		return nil, fmt.Errorf("nil version response")
	}
	if response.HTTPResponse == nil {
		return nil, fmt.Errorf("empty HTTP response")
	}

	if response.HTTPResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("version endpoint returned %d %s: %s",
			response.HTTPResponse.StatusCode,
			http.StatusText(response.HTTPResponse.StatusCode),
			string(response.Body))
	}

	if response.JSON200 == nil {
		return nil, fmt.Errorf("missing version payload")
	}

	return &api.Version{Version: response.JSON200.Version}, nil
}

// CreateCertificateSigningRequestWithBodyWithResponse wraps the original method with version checking
func (c *VersionAwareClient) CreateCertificateSigningRequestWithBodyWithResponse(ctx context.Context, contentType string, body io.Reader) (*apiclient.CreateCertificateSigningRequestResponse, error) {
	if err := c.CheckVersionCompatibility(ctx); err != nil {
		return nil, err
	}

	return c.ClientWithResponses.CreateCertificateSigningRequestWithBodyWithResponse(ctx, contentType, body)
}
