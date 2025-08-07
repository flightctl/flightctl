package client

import (
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestResolvePullSecret(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name                 string
		deviceSpec           *v1alpha1.DeviceSpec
		authPath             string
		setupOnDiskAuth      func(fileio.ReadWriter) // function to write an on-disk auth file
		expectedFound        bool
		expectedError        bool
		expectTmpFileCleanup bool
	}{
		{
			name: "inline auth found in device spec",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "auth-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/containers/auth.json",
						Content: `{"auths":{"registry.redhat.io":{"username":"user","password":"pass"}}}`,
					},
				}),
			}),
			authPath:             "/etc/containers/auth.json",
			expectedFound:        true,
			expectTmpFileCleanup: true,
		},
		{
			name: "inline auth found with whitespace in path",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "auth-config", []v1alpha1.FileSpec{
					{
						Path:    "  /etc/containers/auth.json  ",
						Content: `{"auths":{"example.com":{"auth":"dXNlcjpwYXNz"}}}`,
					},
				}),
			}),
			authPath:             "/etc/containers/auth.json",
			expectedFound:        true,
			expectTmpFileCleanup: true,
		},
		{
			name: "multiple config providers - auth found in second provider",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "other-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/other/config.yaml",
						Content: "some: config",
					},
				}),
				createInlineConfigProvider(require, "auth-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/containers/auth.json",
						Content: `{"auths":{"quay.io":{"username":"test","password":"secret"}}}`,
					},
				}),
			}),
			authPath:             "/etc/containers/auth.json",
			expectedFound:        true,
			expectTmpFileCleanup: true,
		},
		{
			name: "no inline auth - on-disk file exists",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "other-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/other/config.json",
						Content: `{"other": "config"}`,
					},
				}),
			}),
			authPath: "/etc/containers/auth.json",
			setupOnDiskAuth: func(rw fileio.ReadWriter) {
				// create the on-disk auth file
				err := rw.WriteFile("/etc/containers/auth.json", []byte(`{"auths":{"on-disk.registry":{"auth":"disktoken"}}}`), 0644)
				if err != nil {
					panic(err)
				}
			},
			expectedFound: true,
		},
		{
			name: "no inline auth - on-disk file does not exist",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "other-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/other/file.txt",
						Content: "content",
					},
				}),
			}),
			authPath:      "/etc/containers/auth.json",
			expectedFound: false,
		},
		{
			name:          "nil device config",
			deviceSpec:    &v1alpha1.DeviceSpec{Config: nil},
			authPath:      "/etc/containers/auth.json",
			expectedFound: false,
		},
		{
			name: "empty config providers",
			deviceSpec: &v1alpha1.DeviceSpec{
				Config: &[]v1alpha1.ConfigProviderSpec{},
			},
			authPath: "/etc/containers/auth.json",
			setupOnDiskAuth: func(rw fileio.ReadWriter) {
				err := rw.WriteFile("/etc/containers/auth.json", []byte(`{"auths":{"empty-config.registry":{"auth":"emptytoken"}}}`), 0644)
				if err != nil {
					panic(err)
				}
			},
			expectedFound: true,
		},
		{
			name: "config provider with AsInlineConfigProviderSpec error",
			deviceSpec: &v1alpha1.DeviceSpec{
				Config: &[]v1alpha1.ConfigProviderSpec{
					{}, // invalid provider
				},
			},
			authPath:      "/etc/containers/auth.json",
			expectedFound: false,
			expectedError: true,
		},
		{
			name: "multiple files in inline config - auth not found",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "system-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/systemd/system/myservice.service",
						Content: "[Unit]\nDescription=My Service",
					},
					{
						Path:    "/etc/hostname",
						Content: "myhost",
					},
				}),
			}),
			authPath: "/etc/containers/auth.json",
			setupOnDiskAuth: func(rw fileio.ReadWriter) {
				err := rw.WriteFile("/etc/containers/auth.json", []byte(`{"auths":{"fallback.registry":{"auth":"fallbacktoken"}}}`), 0644)
				if err != nil {
					panic(err)
				}
			},
			expectedFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			rw := fileio.NewReadWriter(fileio.WithTestRootDir(tmpDir))

			// write an on-disk auth file if set
			if tt.setupOnDiskAuth != nil {
				tt.setupOnDiskAuth(rw)
			}
			log := log.NewPrefixLogger("test")

			result, found, err := ResolvePullSecret(log, rw, tt.deviceSpec, tt.authPath)
			if tt.expectedError {
				require.Error(err)
				require.Nil(result)
				require.False(found)
				return
			}

			require.NoError(err)
			require.Equal(tt.expectedFound, found)

			if !tt.expectedFound {
				require.Nil(result)
			} else {
				require.NotNil(result)

				// read the auth file content to verify it's correct
				require.NotEmpty(result.Path)
				content, err := rw.ReadFile(result.Path)
				require.NoError(err)
				require.NotEmpty(content)
				require.NotNil(result.Cleanup)

				// verify cleanup removes the file for temporary files
				if tt.expectTmpFileCleanup {
					exists, err := rw.PathExists(result.Path)
					require.NoError(err)
					require.True(exists)

					result.Cleanup()
					exists, err = rw.PathExists(result.Path)
					require.NoError(err)
					require.False(exists, "temporary file should be removed after cleanup")
				} else {
					// for on-disk files, cleanup should be a no-op
					result.Cleanup()
					exists, err := rw.PathExists(result.Path)
					require.NoError(err)
					require.True(exists, "on-disk file should not be removed by cleanup")
				}
			}
		})
	}
}

func TestAuthFromSpec(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name          string
		deviceSpec    *v1alpha1.DeviceSpec
		authPath      string
		expectedAuth  string
		expectedFound bool
		expectedError bool
	}{
		{
			name: "auth found in first provider",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "auth-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/containers/auth.json",
						Content: `{"auths":{"registry.com":{"auth":"token123"}}}`,
					},
				}),
			}),
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  `{"auths":{"registry.com":{"auth":"token123"}}}`,
			expectedFound: true,
		},
		{
			name: "auth found with path trimming",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "auth-config", []v1alpha1.FileSpec{
					{
						Path:    "  /etc/containers/auth.json  \n\t",
						Content: `{"auths":{"example.org":{"username":"test"}}}`,
					},
				}),
			}),
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  `{"auths":{"example.org":{"username":"test"}}}`,
			expectedFound: true,
		},
		{
			name: "auth not found different path",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "registry-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/containers/registries.conf",
						Content: "unqualified-search-registries = ['docker.io']",
					},
				}),
			}),
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  "",
			expectedFound: false,
		},
		{
			name: "nil config",
			deviceSpec: &v1alpha1.DeviceSpec{
				Config: nil,
			},
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  "",
			expectedFound: false,
		},
		{
			name: "empty providers",
			deviceSpec: &v1alpha1.DeviceSpec{
				Config: &[]v1alpha1.ConfigProviderSpec{},
			},
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  "",
			expectedFound: false,
		},
		{
			name: "provider conversion error handled",
			deviceSpec: &v1alpha1.DeviceSpec{
				Config: &[]v1alpha1.ConfigProviderSpec{
					{}, // invalid provider
					createInlineConfigProvider(require, "auth-config", []v1alpha1.FileSpec{
						{
							Path:    "/etc/containers/auth.json",
							Content: `{"auths":{"valid.registry":{"auth":"validtoken"}}}`,
						},
					}),
				},
			},
			authPath:      "/etc/containers/auth.json",
			expectedFound: false,
			expectedError: true,
		},
		{
			name: "multiple providers - auth found in second",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "hostname-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/hostname",
						Content: "testhost",
					},
				}),
				createInlineConfigProvider(require, "auth-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/containers/auth.json",
						Content: `{"auths":{"second.registry":{"password":"secret"}}}`,
					},
				}),
			}),
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  `{"auths":{"second.registry":{"password":"secret"}}}`,
			expectedFound: true,
		},
		{
			name: "multiple files in provider - auth found",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1alpha1.ConfigProviderSpec{
				createInlineConfigProvider(require, "multi-config", []v1alpha1.FileSpec{
					{
						Path:    "/etc/systemd/system/test.service",
						Content: "[Unit]\nDescription=Test",
					},
					{
						Path:    "/etc/containers/auth.json",
						Content: `{"auths":{"multi.registry":{"token":"abc123"}}}`,
					},
					{
						Path:    "/etc/motd",
						Content: "Welcome to the system",
					},
				}),
			}),
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  `{"auths":{"multi.registry":{"token":"abc123"}}}`,
			expectedFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			auth, found, err := authFromSpec(log, tt.deviceSpec, tt.authPath)
			if tt.expectedError {
				require.Error(err)
			} else {
				require.NoError(err)
			}
			require.Equal(tt.expectedFound, found)
			require.Equal(tt.expectedAuth, string(auth))
		})
	}
}

func createInlineConfigProvider(require *require.Assertions, name string, files []v1alpha1.FileSpec) v1alpha1.ConfigProviderSpec {
	var provider v1alpha1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(v1alpha1.InlineConfigProviderSpec{
		Name:   name,
		Inline: files,
	})
	require.NoError(err)
	return provider
}

func createDeviceSpecWithInlineConfig(providers []v1alpha1.ConfigProviderSpec) *v1alpha1.DeviceSpec {
	return &v1alpha1.DeviceSpec{
		Config: &providers,
	}
}
