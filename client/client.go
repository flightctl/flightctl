package client

import (
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
