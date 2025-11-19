package aap

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	gatewayUrl  string
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
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: options.TLSClientConfig,
		},
		Timeout: defaultTimeout,
	}

	return &AAPGatewayClient{
		client:      client,
		gatewayUrl:  options.GatewayUrl,
		maxPageSize: options.MaxPageSize,
	}, nil
}

func (a *AAPGatewayClient) buildURL(path string) string {
	return fmt.Sprintf("%s%s", a.gatewayUrl, path)
}

func (a *AAPGatewayClient) appendQueryParams(path string) string {
	if a.maxPageSize != nil {
		return fmt.Sprintf("%s?page_size=%d", path, *a.maxPageSize)
	}
	return path
}

func get[T any](a *AAPGatewayClient, path string, token string) (*T, error) {
	url := a.buildURL(path)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Add(common.AuthHeader, fmt.Sprintf("Bearer %s", token))
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

func getWithPagination[T any](a *AAPGatewayClient, path string, token string) ([]*T, error) {
	result, err := get[AAPPaginatedResponse[T]](a, path, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get with pagination: %w", err)
	}

	items := result.Results

	if result.Next != nil {
		nextResult, err := getWithPagination[T](a, *result.Next, token)
		if err != nil {
			return nil, fmt.Errorf("failed to get next page: %w", err)
		}
		items = append(items, nextResult...)
	}

	return items, nil
}
