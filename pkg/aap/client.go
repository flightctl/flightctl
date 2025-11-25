package aap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
)

const defaultTimeout = 10 * time.Second

// AAP Gateway API response types
type AAPPaginatedResponse[T any] struct {
	Count    int     `json:"count"`
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
	Results  []*T    `json:"results"`
}

type AAPGatewayClient struct {
	gatewayURL  *url.URL
	client      *http.Client
	maxPageSize *int
}

type AAPGatewayClientOptions struct {
	GatewayUrl      string
	TLSClientConfig *tls.Config
	MaxPageSize     *int
}

func NewAAPGatewayClient(options AAPGatewayClientOptions) (*AAPGatewayClient, error) {
	if options.GatewayUrl == "" {
		return nil, errors.New("aap_client: GatewayUrl is required")
	}
	if options.TLSClientConfig == nil {
		return nil, errors.New("aap_client: TLSClientConfig is required")
	}

	// Parse gateway URL once during initialization
	gatewayURL, err := url.Parse(options.GatewayUrl)
	if err != nil {
		return nil, fmt.Errorf("aap_client: invalid GatewayUrl: %w", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: options.TLSClientConfig,
		},
		Timeout: defaultTimeout,
	}

	return &AAPGatewayClient{
		client:      client,
		gatewayURL:  gatewayURL,
		maxPageSize: options.MaxPageSize,
	}, nil
}

// buildEndpoint constructs a full URL for an API endpoint
// path: the API path (e.g., "/api/gateway/v1/me/")
// query: optional query parameters
func (a *AAPGatewayClient) buildEndpoint(path string, query url.Values) *url.URL {
	// Create a new URL based on the gateway URL
	endpoint := &url.URL{
		Scheme: a.gatewayURL.Scheme,
		Host:   a.gatewayURL.Host,
		Path:   path,
	}

	// Add query parameters if provided
	if len(query) > 0 {
		endpoint.RawQuery = query.Encode()
	}

	return endpoint
}

func get[T any](a *AAPGatewayClient, ctx context.Context, endpoint *url.URL, token string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(common.AuthHeader, fmt.Sprintf("Bearer %s", token))
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, ErrForbidden
		}
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response body: %w", err)
	}

	return &result, nil
}

func getWithPagination[T any](a *AAPGatewayClient, ctx context.Context, endpoint *url.URL, token string) ([]*T, error) {
	result, err := get[AAPPaginatedResponse[T]](a, ctx, endpoint, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get with pagination: %w", err)
	}

	items := result.Results

	if result.Next != nil && *result.Next != "" {
		// AAP returns absolute URLs for pagination, so parse the full URL
		nextURL, err := url.Parse(*result.Next)
		if err != nil {
			return nil, fmt.Errorf("failed to parse next page URL: %w", err)
		}

		nextResult, err := getWithPagination[T](a, ctx, nextURL, token)
		if err != nil {
			return nil, fmt.Errorf("failed to get next page: %w", err)
		}
		items = append(items, nextResult...)
	}

	return items, nil
}
