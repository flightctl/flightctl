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

func TestBuildOAuthApplicationRequest(t *testing.T) {
	tests := []struct {
		name         string
		baseDomain   string
		appName      string
		organization int
	}{
		{
			name:         "upstream default app name",
			baseDomain:   "example.com",
			appName:      "Flight Control",
			organization: 1,
		},
		{
			name:         "downstream override app name",
			baseDomain:   "example.com",
			appName:      "Edge Manager",
			organization: 1,
		},
		{
			name:         "custom app name with custom org",
			baseDomain:   "custom.example.com",
			appName:      "My Custom App",
			organization: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildOAuthApplicationRequest(tt.baseDomain, tt.appName, tt.organization)

			require.Equal(t, tt.appName, req.Name)
			require.Equal(t, tt.organization, req.Organization)
			require.Equal(t, "authorization-code", req.AuthorizationGrantType)
			require.Equal(t, "public", req.ClientType)
			require.Equal(t, "https://"+tt.baseDomain+":443", req.AppURL)
			require.Contains(t, req.RedirectURIs, "https://"+tt.baseDomain+":443/callback")
			require.Contains(t, req.RedirectURIs, "http://localhost:8080/callback")
			require.Contains(t, req.RedirectURIs, "http://127.0.0.1:8080/callback")
		})
	}
}

func TestCreateAAPApplicationWithDownstreamName(t *testing.T) {
	require := require.New(t)

	// Verify that a downstream app name (e.g. "Edge Manager") flows
	// through the full creation pipeline correctly. This exercises the
	// path that is activated when DEFAULT_AAP_APP_NAME is set at build
	// time via Makefile ldflags.
	const downstreamAppName = "Edge Manager"

	mock := &MockOAuthApplicationCreator{
		CreateFunc: func(ctx context.Context, token string, req *aap.AAPOAuthApplicationRequest) (*aap.AAPOAuthApplicationResponse, error) {
			require.Equal(downstreamAppName, req.Name, "OAuth application request should use the downstream app name")
			return &aap.AAPOAuthApplicationResponse{
				ID:       1,
				Name:     req.Name,
				ClientID: "downstream-client-id",
			}, nil
		},
	}

	outputPath := filepath.Join(t.TempDir(), "client_id")
	err := CreateAAPApplication(context.Background(), CreateAAPApplicationOptions{
		Client: mock,
		Logger: log.NewPrefixLogger("test"),
		AAPConfig: &standaloneconfig.AAPConfig{
			Token: "test-token",
		},
		BaseDomain:   "edge.example.com",
		AppName:      downstreamAppName,
		Organization: 1,
		OutputFile:   outputPath,
	})

	require.NoError(err)

	data, err := os.ReadFile(outputPath)
	require.NoError(err)
	require.Equal("downstream-client-id", string(data))
}
