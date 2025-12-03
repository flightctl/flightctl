package client

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	internalclient "github.com/flightctl/flightctl/internal/client"
)

// ClientOption is a functional option for configuring the client.
type ClientOption func(*internalclient.Config)

// WithToken configures the client to use bearer token authentication.
func WithToken(token string) ClientOption {
	return func(c *internalclient.Config) {
		c.AuthInfo.AccessToken = token
	}
}

// WithInsecureSkipVerify disables TLS certificate verification.
// WARNING: Only use this for testing/development. Production clients should verify certificates.
func WithInsecureSkipVerify() ClientOption {
	return func(c *internalclient.Config) {
		c.Service.InsecureSkipVerify = true
	}
}

// NewClient creates a FlightCtl API client.
func NewClient(server string, opts ...ClientOption) (*apiclient.ClientWithResponses, error) {
	config := &internalclient.Config{
		Service: internalclient.Service{
			Server:             server,
			InsecureSkipVerify: false,
		},
	}
	for _, opt := range opts {
		opt(config)
	}
	return internalclient.NewFromConfig(config, "")
}

// GetDevice fetches a device by name.
func GetDevice(ctx context.Context, client *apiclient.ClientWithResponses, name string) (*apiclient.GetDeviceResponse, error) {
	response, err := client.GetDeviceWithResponse(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("getting device %s: %w", name, err)
	}
	return response, nil
}

// ApplyRepository creates or updates a repository.
func ApplyRepository(ctx context.Context, client *apiclient.ClientWithResponses, repo v1beta1.Repository) (*v1beta1.Repository, error) {
	if repo.Metadata.Name == nil {
		return nil, fmt.Errorf("repository name is required")
	}
	name := *repo.Metadata.Name
	getResp, err := client.GetRepositoryWithResponse(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("getting repository %s: %w", name, err)
	}

	if getResp.StatusCode() == 404 {
		createResp, err := client.CreateRepositoryWithResponse(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("creating repository %s: %w", name, err)
		}
		if createResp.JSON201 != nil {
			return createResp.JSON201, nil
		}
		return nil, fmt.Errorf("creating repository %s: unexpected status %d", name, createResp.StatusCode())
	}

	if getResp.JSON200 == nil {
		return nil, fmt.Errorf("getting repository %s: unexpected status %d", name, getResp.StatusCode())
	}

	repo.Metadata.ResourceVersion = getResp.JSON200.Metadata.ResourceVersion

	updateResp, err := client.ReplaceRepositoryWithResponse(ctx, name, repo)
	if err != nil {
		return nil, fmt.Errorf("updating repository %s: %w", name, err)
	}
	if updateResp.JSON200 != nil {
		return updateResp.JSON200, nil
	}
	return nil, fmt.Errorf("updating repository %s: unexpected status %d", name, updateResp.StatusCode())
}
