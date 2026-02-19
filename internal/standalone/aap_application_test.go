package standalone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	standaloneconfig "github.com/flightctl/flightctl/internal/config/standalone"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

type MockOAuthApplicationCreator struct {
	CreateFunc func(ctx context.Context, token string, req *aap.AAPOAuthApplicationRequest) (*aap.AAPOAuthApplicationResponse, error)
}

func (m *MockOAuthApplicationCreator) CreateOAuthApplication(ctx context.Context, token string, req *aap.AAPOAuthApplicationRequest) (*aap.AAPOAuthApplicationResponse, error) {
	return m.CreateFunc(ctx, token, req)
}

func TestCreateAAPApplication(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name          string
		setupMock     func() *MockOAuthApplicationCreator
		baseDomain    string
		appName       string
		organization  int
		outputPath    func(t *testing.T) string
		expectedError string
		verifyFile    func(t *testing.T, path string)
	}{
		{
			name: "success - OAuth app created and client_id written to file",
			setupMock: func() *MockOAuthApplicationCreator {
				return &MockOAuthApplicationCreator{
					CreateFunc: func(ctx context.Context, token string, req *aap.AAPOAuthApplicationRequest) (*aap.AAPOAuthApplicationResponse, error) {
						return &aap.AAPOAuthApplicationResponse{
							ID:                     1,
							Name:                   req.Name,
							ClientID:               "test-client-id-12345",
							ClientType:             req.ClientType,
							AuthorizationGrantType: req.AuthorizationGrantType,
							RedirectURIs:           req.RedirectURIs,
							AppURL:                 req.AppURL,
							Organization:           req.Organization,
						}, nil
					},
				}
			},
			baseDomain:   "example.com",
			appName:      "test-app",
			organization: 1,
			outputPath: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "client_id")
			},
			verifyFile: func(t *testing.T, path string) {
				data, err := os.ReadFile(path)
				require.NoError(err)
				require.Equal("test-client-id-12345", string(data))
			},
		},
		{
			name: "client error - CreateOAuthApplication returns error",
			setupMock: func() *MockOAuthApplicationCreator {
				return &MockOAuthApplicationCreator{
					CreateFunc: func(ctx context.Context, token string, req *aap.AAPOAuthApplicationRequest) (*aap.AAPOAuthApplicationResponse, error) {
						return nil, errors.New("connection refused")
					},
				}
			},
			baseDomain:   "example.com",
			appName:      "test-app",
			organization: 1,
			outputPath: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "client_id")
			},
			expectedError: "failed to create OAuth application",
		},
		{
			name: "empty client_id - AAP returns empty ClientID",
			setupMock: func() *MockOAuthApplicationCreator {
				return &MockOAuthApplicationCreator{
					CreateFunc: func(ctx context.Context, token string, req *aap.AAPOAuthApplicationRequest) (*aap.AAPOAuthApplicationResponse, error) {
						return &aap.AAPOAuthApplicationResponse{
							ID:       1,
							Name:     req.Name,
							ClientID: "",
						}, nil
					},
				}
			},
			baseDomain:   "example.com",
			appName:      "test-app",
			organization: 1,
			outputPath: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "client_id")
			},
			expectedError: "AAP returned empty client_id",
		},
		{
			name: "file write failure - invalid output path",
			setupMock: func() *MockOAuthApplicationCreator {
				return &MockOAuthApplicationCreator{
					CreateFunc: func(ctx context.Context, token string, req *aap.AAPOAuthApplicationRequest) (*aap.AAPOAuthApplicationResponse, error) {
						return &aap.AAPOAuthApplicationResponse{
							ID:       1,
							Name:     req.Name,
							ClientID: "test-client-id",
						}, nil
					},
				}
			},
			baseDomain:   "example.com",
			appName:      "test-app",
			organization: 1,
			outputPath: func(t *testing.T) string {
				return "/nonexistent/directory/client_id"
			},
			expectedError: "failed to write client_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			logger := log.NewPrefixLogger("test")
			mock := tt.setupMock()
			outputPath := tt.outputPath(t)

			opts := CreateAAPApplicationOptions{
				Client: mock,
				Logger: logger,
				AAPConfig: &standaloneconfig.AAPConfig{
					Token: "test-token",
				},
				BaseDomain:   tt.baseDomain,
				AppName:      tt.appName,
				Organization: tt.organization,
				OutputFile:   outputPath,
			}

			err := CreateAAPApplication(ctx, opts)

			if tt.expectedError != "" {
				require.Error(err)
				require.Contains(err.Error(), tt.expectedError)
			} else {
				require.NoError(err)
				if tt.verifyFile != nil {
					tt.verifyFile(t, outputPath)
				}
			}
		})
	}
}
