package cli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mockRoundTripper is a custom http.RoundTripper for mocking API responses.
type mockRoundTripper struct {
	roundTrip func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTrip(req)
}

func TestLoginRun_NoOrganizations(t *testing.T) {
	require := require.New(t)

	// Create a mock HTTP client that returns an empty list of organizations.
	mockClient := &http.Client{
		Transport: &mockRoundTripper{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				var resp *http.Response
				if strings.HasSuffix(req.URL.Path, "/api/v1/organizations") {
					resp = &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{ "items": [] }`)),
						Header:     make(http.Header),
					}
				} else if strings.HasSuffix(req.URL.Path, "/api/v1/auth/validate") {
					resp = &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{}`)),
						Header:     make(http.Header),
					}
				} else { // AuthConfig
					resp = &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"providers":[{"metadata":{"name":"test-provider"}, "spec":{"type":"oidc"}}]}`)),
						Header:     make(http.Header),
					}
				}

				resp.Header.Set("Content-Type", "application/json")
				return resp, nil
			},
		},
	}

	// Create an HTTP client option to inject the mock client.
	httpClientOption := func(c *http.Client) error {
		c.Transport = mockClient.Transport
		return nil
	}

	// Create LoginOptions with the mock client.
	o := DefaultLoginOptions()
	err := o.Init([]string{"https://test.com"})
	require.NoError(err)
	o.AccessToken = "test-token"
	o.clientConfig.AddHTTPOptions(httpClientOption)

	// Run the login command.
	err = o.Run(context.Background(), []string{"https://test.com"})

	// Check for the expected error message.
	require.Error(t, err)
	expectedError := "Unable to log in to the application\n\nYou do not have access to any organizations.\n\nPlease contact your administrator to be granted access to an organization."
	require.Equal(t, expectedError, err.Error())
}