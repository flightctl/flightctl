package client

import (
	"context"
	"fmt"
	"net/http"

	client "github.com/flightctl/flightctl/internal/api/client/agent"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/middleware"
)

// NewFromConfig returns a new FlightCtl API client from the given config.
func NewFromConfig(config *baseclient.Config) (*client.ClientWithResponses, error) {

	httpClient, err := baseclient.NewHTTPClientFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewFromConfig: creating HTTP client %w", err)
	}
	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.GetReqID())
		return nil
	})
	return client.NewClientWithResponses(config.Service.Server, client.WithHTTPClient(httpClient), ref)
}

type Config = baseclient.Config
type AuthInfo = baseclient.AuthInfo
type Service = baseclient.Service

func NewDefault() *Config {
	return baseclient.NewDefault()
}
