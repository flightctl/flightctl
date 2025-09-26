package aap

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
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
