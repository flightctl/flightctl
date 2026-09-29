package aap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAAPGatewayClient(t *testing.T) {
	pageSize := 100
	testCases := []struct {
		name        string
		options     AAPGatewayClientOptions
		expectError bool
		verifyFunc  func(t *testing.T, client *AAPGatewayClient)
	}{
		{
			name: "no gateway url",
			options: AAPGatewayClientOptions{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13},
			},
			expectError: true,
		},
		{
			name: "no tls client config",
			options: AAPGatewayClientOptions{
				GatewayUrl: "https://example.com",
			},
			expectError: true,
		},
		{
			name: "no page size",
			options: AAPGatewayClientOptions{
				GatewayUrl:      "https://example.com",
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13},
			},
			expectError: false,
			verifyFunc: func(t *testing.T, client *AAPGatewayClient) {
				assert.Nil(t, client.maxPageSize)
			},
		},
		{
			name: "with page size",
			options: AAPGatewayClientOptions{
				GatewayUrl:      "https://example.com",
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13},
				MaxPageSize:     &pageSize,
			},
			expectError: false,
			verifyFunc: func(t *testing.T, client *AAPGatewayClient) {
				assert.Equal(t, &pageSize, client.maxPageSize)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewAAPGatewayClient(tc.options)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				tc.verifyFunc(t, client)
			}
		})
	}
}

func TestGetWithPagination(t *testing.T) {
	const apiPath = "/api/gateway/v1/organizations"

	testCases := []struct {
		name          string
		pages         []pageSpec
		expectedNames []string
	}{
		{
			name: "When next is nil it should return only the first page",
			pages: []pageSpec{
				{
					orgs: []AAPOrganization{{ID: 1, Name: "org-1"}, {ID: 2, Name: "org-2"}},
					next: nextNil,
				},
			},
			expectedNames: []string{"org-1", "org-2"},
		},
		{
			name: "When next is empty it should return only the first page",
			pages: []pageSpec{
				{
					orgs: []AAPOrganization{{ID: 1, Name: "org-1"}},
					next: nextEmpty,
				},
			},
			expectedNames: []string{"org-1"},
		},
		{
			name: "When next is an absolute URL it should follow pagination",
			pages: []pageSpec{
				{
					orgs: []AAPOrganization{{ID: 1, Name: "org-1"}},
					next: nextAbsolute,
				},
				{
					orgs: []AAPOrganization{{ID: 2, Name: "org-2"}},
					next: nextNil,
				},
			},
			expectedNames: []string{"org-1", "org-2"},
		},
		{
			name: "When next is a relative URL it should resolve against the gateway base URL",
			pages: []pageSpec{
				{
					orgs: []AAPOrganization{{ID: 1, Name: "org-1"}},
					next: nextRelative,
				},
				{
					orgs: []AAPOrganization{{ID: 2, Name: "org-2"}},
					next: nextNil,
				},
			},
			expectedNames: []string{"org-1", "org-2"},
		},
		{
			name: "When pages mix absolute and relative next URLs it should aggregate all results",
			pages: []pageSpec{
				{
					orgs: []AAPOrganization{{ID: 1, Name: "org-1"}, {ID: 2, Name: "org-2"}},
					next: nextAbsolute,
				},
				{
					orgs: []AAPOrganization{{ID: 3, Name: "org-3"}, {ID: 4, Name: "org-4"}},
					next: nextRelative,
				},
				{
					orgs: []AAPOrganization{{ID: 5, Name: "org-5"}},
					next: nextNil,
				},
			},
			expectedNames: []string{"org-1", "org-2", "org-3", "org-4", "org-5"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := newPaginatedServer(t, apiPath, tc.pages)
			defer server.Close()

			client, err := NewAAPGatewayClient(AAPGatewayClientOptions{
				GatewayUrl:      server.URL,
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			})
			require.NoError(t, err)

			orgs, err := client.ListOrganizations(context.Background(), "test-token")
			require.NoError(t, err)

			var names []string
			for _, org := range orgs {
				names = append(names, org.Name)
			}
			assert.Equal(t, tc.expectedNames, names)
		})
	}
}

func TestGetWithPaginationMixedIntegration(t *testing.T) {
	const apiPath = "/api/gateway/v1/organizations"

	// Simulate a 3-page response mixing absolute and relative next URLs,
	// verifying that all items across all pages are aggregated correctly.
	pages := []pageSpec{
		{
			orgs: []AAPOrganization{{ID: 1, Name: "alpha"}, {ID: 2, Name: "bravo"}},
			next: nextAbsolute,
		},
		{
			orgs: []AAPOrganization{{ID: 3, Name: "charlie"}, {ID: 4, Name: "delta"}},
			next: nextRelative,
		},
		{
			orgs: []AAPOrganization{{ID: 5, Name: "echo"}},
			next: nextNil,
		},
	}

	server := newPaginatedServer(t, apiPath, pages)
	defer server.Close()

	client, err := NewAAPGatewayClient(AAPGatewayClientOptions{
		GatewayUrl:      server.URL,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	})
	require.NoError(t, err)

	orgs, err := client.ListOrganizations(context.Background(), "test-token")
	require.NoError(t, err)
	require.Len(t, orgs, 5)

	expectedIDs := []int{1, 2, 3, 4, 5}
	expectedNames := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for i, org := range orgs {
		assert.Equal(t, expectedIDs[i], org.ID)
		assert.Equal(t, expectedNames[i], org.Name)
	}
}

// nextKind controls how the mock server generates the next field for each page.
type nextKind int

const (
	nextNil      nextKind = iota // next is nil — terminates pagination
	nextEmpty                    // next is "" — terminates pagination
	nextAbsolute                 // next is an absolute URL (scheme + host + path)
	nextRelative                 // next is a relative URL (path only)
)

// pageSpec describes a single page of paginated results.
type pageSpec struct {
	orgs []AAPOrganization
	next nextKind
}

// newPaginatedServer creates an httptest.TLSServer that serves paginated
// organization responses. Each page is distinguished by a "page" query
// parameter (defaulting to 1).
func newPaginatedServer(t *testing.T, apiPath string, pages []pageSpec) *httptest.Server {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")

		pageNum := 1
		if p := r.URL.Query().Get("page"); p != "" {
			_, err := fmt.Sscanf(p, "%d", &pageNum)
			require.NoError(t, err)
		}

		if pageNum < 1 || pageNum > len(pages) {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		page := pages[pageNum-1]
		results := make([]*AAPOrganization, len(page.orgs))
		for i := range page.orgs {
			results[i] = &page.orgs[i]
		}

		resp := AAPPaginatedResponse[AAPOrganization]{
			Count:   len(page.orgs),
			Results: results,
		}

		switch page.next {
		case nextNil:
			resp.Next = nil
		case nextEmpty:
			empty := ""
			resp.Next = &empty
		case nextAbsolute:
			abs := fmt.Sprintf("https://%s%s?page=%d", r.Host, apiPath, pageNum+1)
			resp.Next = &abs
		case nextRelative:
			rel := fmt.Sprintf("%s?page=%d", apiPath, pageNum+1)
			resp.Next = &rel
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		respBytes, err := json.Marshal(resp)
		require.NoError(t, err)
		_, err = w.Write(respBytes)
		require.NoError(t, err)
	}))

	return server
}
