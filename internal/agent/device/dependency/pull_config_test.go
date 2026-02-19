package dependency

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestPullConfigResolver_Options(t *testing.T) {
	tests := []struct {
		name                 string
		deviceSpec           *v1beta1.DeviceSpec
		authPath             string
		setupOnDiskAuth      func(fileio.ReadWriter)
		expectedFound        bool
		expectTmpFileCleanup bool
	}{
		{
			name: "inline auth found in device spec",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "auth-config", []v1beta1.FileSpec{
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
			name: "multiple config providers - auth found in second provider",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "other-config", []v1beta1.FileSpec{
					{
						Path:    "/etc/other/config.yaml",
						Content: "some: config",
					},
				}),
				createInlineConfigProvider(t, "auth-config", []v1beta1.FileSpec{
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
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "other-config", []v1beta1.FileSpec{
					{
						Path:    "/etc/other/config.json",
						Content: `{"other": "config"}`,
					},
				}),
			}),
			authPath: "/etc/containers/auth.json",
			setupOnDiskAuth: func(rw fileio.ReadWriter) {
				err := rw.WriteFile("/etc/containers/auth.json", []byte(`{"auths":{"on-disk.registry":{"auth":"disktoken"}}}`), 0644)
				if err != nil {
					panic(err)
				}
			},
			expectedFound: true,
		},
		{
			name: "no inline auth - on-disk file does not exist",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "other-config", []v1beta1.FileSpec{
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
			deviceSpec:    &v1beta1.DeviceSpec{Config: nil},
			authPath:      "/etc/containers/auth.json",
			expectedFound: false,
		},
		{
			name: "empty config providers",
			deviceSpec: &v1beta1.DeviceSpec{
				Config: &[]v1beta1.ConfigProviderSpec{},
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
			name: "multiple files in inline config - auth not found uses on-disk",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "system-config", []v1beta1.FileSpec{
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
		{
			name: "inline content identical to on-disk uses on-disk path",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "auth-config", []v1beta1.FileSpec{
					{
						Path:    "/etc/containers/auth.json",
						Content: `{"auths":{"same.registry":{"auth":"sametoken"}}}`,
					},
				}),
			}),
			authPath: "/etc/containers/auth.json",
			setupOnDiskAuth: func(rw fileio.ReadWriter) {
				err := rw.WriteFile("/etc/containers/auth.json", []byte(`{"auths":{"same.registry":{"auth":"sametoken"}}}`), 0644)
				if err != nil {
					panic(err)
				}
			},
			expectedFound:        true,
			expectTmpFileCleanup: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()

			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

			if tt.setupOnDiskAuth != nil {
				tt.setupOnDiskAuth(rw)
			}

			testLog := log.NewPrefixLogger("test")
			rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
				return rw, nil
			}

			resolver := NewPullConfigResolver(testLog, rwFactory)
			resolver.BeforeUpdate(tt.deviceSpec)

			var resolvedPath string
			optsFn := resolver.Options(PullConfigSpec{
				Paths: []string{tt.authPath},
				OptionFn: func(path string) client.ClientOption {
					resolvedPath = path
					return client.WithPullSecret(path)
				},
			})

			opts := optsFn()

			if !tt.expectedFound {
				require.Empty(opts)
				require.Empty(resolvedPath)
			} else {
				require.Len(opts, 1)
				require.NotEmpty(resolvedPath)

				content, err := rw.ReadFile(resolvedPath)
				require.NoError(err)
				require.NotEmpty(content)

				if tt.expectTmpFileCleanup {
					exists, err := rw.PathExists(resolvedPath)
					require.NoError(err)
					require.True(exists)

					resolver.Cleanup()

					exists, err = rw.PathExists(resolvedPath)
					require.NoError(err)
					require.False(exists, "temporary file should be removed after cleanup")
				} else {
					resolver.Cleanup()
					exists, err := rw.PathExists(resolvedPath)
					require.NoError(err)
					require.True(exists, "on-disk file should not be removed by cleanup")
				}
			}
		})
	}
}

func TestPullConfigResolver_AuthFromSpec(t *testing.T) {
	tests := []struct {
		name          string
		deviceSpec    *v1beta1.DeviceSpec
		authPath      string
		expectedAuth  string
		expectedFound bool
	}{
		{
			name: "auth found in first provider",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "auth-config", []v1beta1.FileSpec{
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
			name: "auth not found different path",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "registry-config", []v1beta1.FileSpec{
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
			deviceSpec: &v1beta1.DeviceSpec{
				Config: nil,
			},
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  "",
			expectedFound: false,
		},
		{
			name: "empty providers",
			deviceSpec: &v1beta1.DeviceSpec{
				Config: &[]v1beta1.ConfigProviderSpec{},
			},
			authPath:      "/etc/containers/auth.json",
			expectedAuth:  "",
			expectedFound: false,
		},
		{
			name: "multiple providers - auth found in second",
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "hostname-config", []v1beta1.FileSpec{
					{
						Path:    "/etc/hostname",
						Content: "testhost",
					},
				}),
				createInlineConfigProvider(t, "auth-config", []v1beta1.FileSpec{
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
			deviceSpec: createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
				createInlineConfigProvider(t, "multi-config", []v1beta1.FileSpec{
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
			require := require.New(t)
			tmpDir := t.TempDir()

			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
				fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
			)

			testLog := log.NewPrefixLogger("test")
			rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
				return rw, nil
			}

			resolver := NewPullConfigResolver(testLog, rwFactory).(*pullConfigResolver)
			resolver.BeforeUpdate(tt.deviceSpec)

			authFile := resolver.authFromSpec(tt.deviceSpec, tt.authPath)
			t.Logf("authFile: %v", authFile)
			if tt.expectedFound {
				require.NotNil(authFile)
				auth, err := authFile.ContentsDecoded()
				require.NoError(err)
				require.Equal(tt.expectedAuth, string(auth))
			} else {
				require.Nil(authFile)
			}
		})
	}
}

func TestPullConfigResolver_Cleanup(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	testLog := log.NewPrefixLogger("test")
	rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
		return rw, nil
	}

	deviceSpec := createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
		createInlineConfigProvider(t, "auth-config", []v1beta1.FileSpec{
			{
				Path:    "/etc/containers/auth.json",
				Content: `{"auths":{"cleanup.test":{"auth":"token"}}}`,
			},
		}),
	})

	resolver := NewPullConfigResolver(testLog, rwFactory)
	resolver.BeforeUpdate(deviceSpec)

	var resolvedPath string
	optsFn := resolver.Options(PullConfigSpec{
		Paths: []string{"/etc/containers/auth.json"},
		OptionFn: func(path string) client.ClientOption {
			resolvedPath = path
			return client.WithPullSecret(path)
		},
	})

	opts := optsFn()
	require.Len(opts, 1)
	require.NotEmpty(resolvedPath)

	exists, err := rw.PathExists(resolvedPath)
	require.NoError(err)
	require.True(exists, "temp file should exist before cleanup")

	resolver.Cleanup()

	exists, err = rw.PathExists(resolvedPath)
	require.NoError(err)
	require.False(exists, "temp file should be removed after cleanup")
}

func TestPullConfigResolver_CacheInvalidation(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	testLog := log.NewPrefixLogger("test")
	rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
		return rw, nil
	}

	resolver := NewPullConfigResolver(testLog, rwFactory)

	spec1 := createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
		createInlineConfigProvider(t, "auth-config", []v1beta1.FileSpec{
			{
				Path:    "/etc/containers/auth.json",
				Content: `{"auths":{"v1":{"auth":"token1"}}}`,
			},
		}),
	})

	resolver.BeforeUpdate(spec1)

	var path1 string
	optsFn := resolver.Options(PullConfigSpec{
		Paths: []string{"/etc/containers/auth.json"},
		OptionFn: func(path string) client.ClientOption {
			path1 = path
			return client.WithPullSecret(path)
		},
	})
	optsFn()

	content1, err := rw.ReadFile(path1)
	require.NoError(err)
	require.Contains(string(content1), "v1")

	spec2 := createDeviceSpecWithInlineConfig([]v1beta1.ConfigProviderSpec{
		createInlineConfigProvider(t, "auth-config", []v1beta1.FileSpec{
			{
				Path:    "/etc/containers/auth.json",
				Content: `{"auths":{"v2":{"auth":"token2"}}}`,
			},
		}),
	})

	resolver.BeforeUpdate(spec2)

	var path2 string
	optsFn2 := resolver.Options(PullConfigSpec{
		Paths: []string{"/etc/containers/auth.json"},
		OptionFn: func(path string) client.ClientOption {
			path2 = path
			return client.WithPullSecret(path)
		},
	})
	optsFn2()

	content2, err := rw.ReadFile(path2)
	require.NoError(err)
	require.Contains(string(content2), "v2")

	resolver.Cleanup()
}

func TestPullConfigResolver_FallbackPaths(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	err := rw.WriteFile("/etc/fallback/auth.json", []byte(`{"auths":{"fallback":{"auth":"token"}}}`), 0644)
	require.NoError(err)

	testLog := log.NewPrefixLogger("test")
	rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
		return rw, nil
	}

	resolver := NewPullConfigResolver(testLog, rwFactory)
	resolver.BeforeUpdate(&v1beta1.DeviceSpec{})

	var resolvedPath string
	optsFn := resolver.Options(PullConfigSpec{
		Paths: []string{"/etc/primary/auth.json", "/etc/fallback/auth.json"},
		OptionFn: func(path string) client.ClientOption {
			resolvedPath = path
			return client.WithPullSecret(path)
		},
	})

	opts := optsFn()
	require.Len(opts, 1)
	require.Equal("/etc/fallback/auth.json", resolvedPath)

	resolver.Cleanup()
}

func TestPullConfigResolver_NoDesiredSpec(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()

	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)

	testLog := log.NewPrefixLogger("test")
	rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
		return rw, nil
	}

	resolver := NewPullConfigResolver(testLog, rwFactory)

	optsFn := resolver.Options(PullConfigSpec{
		Paths:    []string{"/etc/containers/auth.json"},
		OptionFn: client.WithPullSecret,
	})

	opts := optsFn()
	require.Empty(opts, "should return empty options when BeforeUpdate not called")
}

func createInlineConfigProvider(t *testing.T, name string, files []v1beta1.FileSpec) v1beta1.ConfigProviderSpec {
	t.Helper()
	var provider v1beta1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{
		Name:   name,
		Inline: files,
	})
	if err != nil {
		t.Fatalf("failed to create inline config provider: %v", err)
	}
	return provider
}

func createDeviceSpecWithInlineConfig(providers []v1beta1.ConfigProviderSpec) *v1beta1.DeviceSpec {
	return &v1beta1.DeviceSpec{
		Config: &providers,
	}
}
