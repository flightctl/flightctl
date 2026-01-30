package client

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestHelm_Version(t *testing.T) {
	testCases := []struct {
		name      string
		setupMock func(*executer.MockExecuter)
		want      *HelmVersion
		wantErr   bool
	}{
		{
			name: "success with standard version",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"version", "--short"}).
					Return("v3.14.0+gc4e7498\n", "", 0)
			},
			want:    &HelmVersion{Major: 3, Minor: 14, Patch: 0},
			wantErr: false,
		},
		{
			name: "success with version without patch",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"version", "--short"}).
					Return("v3.8+g1234567\n", "", 0)
			},
			want:    &HelmVersion{Major: 3, Minor: 8, Patch: 0},
			wantErr: false,
		},
		{
			name: "success with helm 4.x",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"version", "--short"}).
					Return("v4.0.0+gabcdef\n", "", 0)
			},
			want:    &HelmVersion{Major: 4, Minor: 0, Patch: 0},
			wantErr: false,
		},
		{
			name: "error from helm command",
			setupMock: func(mock *executer.MockExecuter) {
				mock.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{"version", "--short"}).
					Return("", "helm: command not found", 127)
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			readWriter := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())

			tc.setupMock(mockExec)
			helm := NewHelm(log, mockExec, readWriter, "/var/lib/flightctl/helm/charts", testBackoffConfig)
			got, err := helm.Version(context.Background())

			if tc.wantErr {
				require.Error(err)
				return
			}

			require.NoError(err)
			require.Equal(tc.want, got)
		})
	}
}

func TestHelmVersion_GreaterOrEqual(t *testing.T) {
	testCases := []struct {
		name    string
		version HelmVersion
		major   int
		minor   int
		want    bool
	}{
		{
			name:    "equal version",
			version: HelmVersion{Major: 3, Minor: 8, Patch: 0},
			major:   3,
			minor:   8,
			want:    true,
		},
		{
			name:    "greater minor version",
			version: HelmVersion{Major: 3, Minor: 14, Patch: 0},
			major:   3,
			minor:   8,
			want:    true,
		},
		{
			name:    "greater major version",
			version: HelmVersion{Major: 4, Minor: 0, Patch: 0},
			major:   3,
			minor:   8,
			want:    true,
		},
		{
			name:    "lesser minor version",
			version: HelmVersion{Major: 3, Minor: 7, Patch: 0},
			major:   3,
			minor:   8,
			want:    false,
		},
		{
			name:    "lesser major version",
			version: HelmVersion{Major: 2, Minor: 17, Patch: 0},
			major:   3,
			minor:   8,
			want:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.version.GreaterOrEqual(tc.major, tc.minor)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseHelmVersion(t *testing.T) {
	testCases := []struct {
		name    string
		input   string
		want    *HelmVersion
		wantErr bool
	}{
		{
			name:    "standard version with build info",
			input:   "v3.14.0+gc4e7498",
			want:    &HelmVersion{Major: 3, Minor: 14, Patch: 0},
			wantErr: false,
		},
		{
			name:    "version without v prefix",
			input:   "3.14.2",
			want:    &HelmVersion{Major: 3, Minor: 14, Patch: 2},
			wantErr: false,
		},
		{
			name:    "version with whitespace",
			input:   "  v3.14.0+gc4e7498  \n",
			want:    &HelmVersion{Major: 3, Minor: 14, Patch: 0},
			wantErr: false,
		},
		{
			name:    "version without patch",
			input:   "v3.8+g1234567",
			want:    &HelmVersion{Major: 3, Minor: 8, Patch: 0},
			wantErr: false,
		},
		{
			name:    "invalid version format",
			input:   "invalid",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "non-numeric major version",
			input:   "vX.14.0",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "pre-release version with rc",
			input:   "v3.14.0-rc.1",
			want:    &HelmVersion{Major: 3, Minor: 14, Patch: 0},
			wantErr: false,
		},
		{
			name:    "pre-release version with beta",
			input:   "v4.0.0-beta.2",
			want:    &HelmVersion{Major: 4, Minor: 0, Patch: 0},
			wantErr: false,
		},
		{
			name:    "pre-release version with alpha and build info",
			input:   "v3.15.0-alpha.1+gc4e7498",
			want:    &HelmVersion{Major: 3, Minor: 15, Patch: 0},
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHelmVersion(tc.input)

			if tc.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestHelm_Pull(t *testing.T) {
	testCases := []struct {
		name      string
		chartRef  string
		destDir   string
		opts      []ClientOption
		setupMock func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name:     "success without auth",
			chartRef: "oci://registry.example.com/charts/myapp:1.0.0",
			destDir:  "/tmp/charts",
			opts:     nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"pull", "oci://registry.example.com/charts/myapp",
					"--untar", "--destination", "/tmp/charts",
					"--version", "1.0.0",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:     "success with auth",
			chartRef: "oci://registry.example.com/charts/myapp:1.0.0",
			destDir:  "/tmp/charts",
			opts:     []ClientOption{WithPullSecret("/path/to/auth.json")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/path/to/auth.json").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"pull", "oci://registry.example.com/charts/myapp",
					"--untar", "--destination", "/tmp/charts",
					"--version", "1.0.0",
					"--registry-config", "/path/to/auth.json",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:     "error from helm pull",
			chartRef: "oci://registry.example.com/charts/myapp:1.0.0",
			destDir:  "/tmp/charts",
			opts:     nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"pull", "oci://registry.example.com/charts/myapp",
					"--untar", "--destination", "/tmp/charts",
					"--version", "1.0.0",
				}).Return("", "Error: chart not found", 1)
			},
			wantErr: true,
		},
		{
			name:     "auth file not found continues without auth",
			chartRef: "oci://registry.example.com/charts/myapp:1.0.0",
			destDir:  "/tmp/charts",
			opts:     []ClientOption{WithPullSecret("/path/to/auth.json")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/path/to/auth.json").Return(false, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"pull", "oci://registry.example.com/charts/myapp",
					"--untar", "--destination", "/tmp/charts",
					"--version", "1.0.0",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)
			helm := NewHelm(log, mockExec, mockReadWriter, "/var/lib/flightctl/helm/charts", testBackoffConfig)
			err := helm.Pull(context.Background(), tc.chartRef, tc.destDir, tc.opts...)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelm_DependencyUpdate(t *testing.T) {
	testCases := []struct {
		name      string
		chartPath string
		opts      []ClientOption
		setupMock func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name:      "success without config",
			chartPath: "/tmp/charts/myapp",
			opts:      nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", "/tmp/charts/myapp",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:      "success with repository config",
			chartPath: "/tmp/charts/myapp",
			opts:      []ClientOption{WithRepositoryConfig("/path/to/repos.yaml")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/path/to/repos.yaml").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", "/tmp/charts/myapp",
					"--repository-config", "/path/to/repos.yaml",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:      "success with both registry and repository config",
			chartPath: "/tmp/charts/myapp",
			opts: []ClientOption{
				WithRepositoryConfig("/path/to/repos.yaml"),
				WithPullSecret("/path/to/auth.json"),
			},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/path/to/repos.yaml").Return(true, nil)
				mockRW.EXPECT().PathExists("/path/to/auth.json").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", "/tmp/charts/myapp",
					"--repository-config", "/path/to/repos.yaml",
					"--registry-config", "/path/to/auth.json",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:      "error from helm dependency update",
			chartPath: "/tmp/charts/myapp",
			opts:      nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", "/tmp/charts/myapp",
				}).Return("", "Error: no repositories found", 1)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)
			helm := NewHelm(log, mockExec, mockReadWriter, "/var/lib/flightctl/helm/charts", testBackoffConfig)
			err := helm.DependencyUpdate(context.Background(), tc.chartPath, tc.opts...)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelm_Install(t *testing.T) {
	testCases := []struct {
		name        string
		releaseName string
		chartPath   string
		opts        []HelmOption
		setupMock   func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr     bool
	}{
		{
			name:        "success without options",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"install", "myapp", "/tmp/charts/myapp",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "success with namespace",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        []HelmOption{WithNamespace("default")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"install", "myapp", "/tmp/charts/myapp",
					"--namespace", "default",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "success with all options",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts: []HelmOption{
				WithNamespace("production"),
				WithCreateNamespace(),
				WithValuesFile("/tmp/values.yaml"),
				WithKubeconfig("/tmp/kubeconfig"),
				WithAtomic(),
			},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/values.yaml").Return(true, nil)
				mockRW.EXPECT().PathExists("/tmp/kubeconfig").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"install", "myapp", "/tmp/charts/myapp",
					"--namespace", "production",
					"--create-namespace",
					"--values", "/tmp/values.yaml",
					"--kubeconfig", "/tmp/kubeconfig",
					"--atomic",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "error - values file not found",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        []HelmOption{WithValuesFile("/tmp/values.yaml")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/values.yaml").Return(false, nil)
			},
			wantErr: true,
		},
		{
			name:        "error - kubeconfig not found",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        []HelmOption{WithKubeconfig("/tmp/kubeconfig")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/kubeconfig").Return(false, nil)
			},
			wantErr: true,
		},
		{
			name:        "error from helm install",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"install", "myapp", "/tmp/charts/myapp",
				}).Return("", "Error: release already exists", 1)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)
			helm := NewHelm(log, mockExec, mockReadWriter, "/var/lib/flightctl/helm/charts", testBackoffConfig)
			err := helm.Install(context.Background(), tc.releaseName, tc.chartPath, tc.opts...)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelm_Upgrade(t *testing.T) {
	testCases := []struct {
		name        string
		releaseName string
		chartPath   string
		opts        []HelmOption
		setupMock   func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr     bool
	}{
		{
			name:        "success without options",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"upgrade", "myapp", "/tmp/charts/myapp",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "success with namespace and values",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts: []HelmOption{
				WithNamespace("production"),
				WithValuesFile("/tmp/values.yaml"),
			},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/values.yaml").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"upgrade", "myapp", "/tmp/charts/myapp",
					"--namespace", "production",
					"--values", "/tmp/values.yaml",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "success with all options",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts: []HelmOption{
				WithNamespace("production"),
				WithValuesFile("/tmp/values.yaml"),
				WithKubeconfig("/tmp/kubeconfig"),
				WithAtomic(),
			},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/values.yaml").Return(true, nil)
				mockRW.EXPECT().PathExists("/tmp/kubeconfig").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"upgrade", "myapp", "/tmp/charts/myapp",
					"--namespace", "production",
					"--values", "/tmp/values.yaml",
					"--kubeconfig", "/tmp/kubeconfig",
					"--atomic",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "error from helm upgrade",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"upgrade", "myapp", "/tmp/charts/myapp",
				}).Return("", "Error: release not found", 1)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)
			helm := NewHelm(log, mockExec, mockReadWriter, "/var/lib/flightctl/helm/charts", testBackoffConfig)
			err := helm.Upgrade(context.Background(), tc.releaseName, tc.chartPath, tc.opts...)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelm_Template(t *testing.T) {
	testCases := []struct {
		name        string
		releaseName string
		chartPath   string
		opts        []HelmOption
		setupMock   func(*executer.MockExecuter, *fileio.MockReadWriter)
		want        string
		wantErr     bool
	}{
		{
			name:        "success without options",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"template", "myapp", "/tmp/charts/myapp", "--skip-tests",
				}).Return("---\napiVersion: v1\nkind: ConfigMap\n", "", 0)
			},
			want:    "---\napiVersion: v1\nkind: ConfigMap\n",
			wantErr: false,
		},
		{
			name:        "success with namespace",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        []HelmOption{WithNamespace("production")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"template", "myapp", "/tmp/charts/myapp", "--skip-tests",
					"--namespace", "production",
				}).Return("---\napiVersion: apps/v1\nkind: Deployment\n", "", 0)
			},
			want:    "---\napiVersion: apps/v1\nkind: Deployment\n",
			wantErr: false,
		},
		{
			name:        "success with values file",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        []HelmOption{WithValuesFile("/tmp/values.yaml")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/values.yaml").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"template", "myapp", "/tmp/charts/myapp", "--skip-tests",
					"--values", "/tmp/values.yaml",
				}).Return("---\napiVersion: v1\nkind: Service\n", "", 0)
			},
			want:    "---\napiVersion: v1\nkind: Service\n",
			wantErr: false,
		},
		{
			name:        "success with namespace and values",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts: []HelmOption{
				WithNamespace("production"),
				WithValuesFile("/tmp/values.yaml"),
			},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/values.yaml").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"template", "myapp", "/tmp/charts/myapp", "--skip-tests",
					"--namespace", "production",
					"--values", "/tmp/values.yaml",
				}).Return("---\napiVersion: apps/v1\nkind: StatefulSet\n", "", 0)
			},
			want:    "---\napiVersion: apps/v1\nkind: StatefulSet\n",
			wantErr: false,
		},
		{
			name:        "error - values file not found",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        []HelmOption{WithValuesFile("/tmp/values.yaml")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/values.yaml").Return(false, nil)
			},
			want:    "",
			wantErr: true,
		},
		{
			name:        "error from helm template",
			releaseName: "myapp",
			chartPath:   "/tmp/charts/myapp",
			opts:        nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"template", "myapp", "/tmp/charts/myapp", "--skip-tests",
				}).Return("", "Error: chart not found", 1)
			},
			want:    "",
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)
			helm := NewHelm(log, mockExec, mockReadWriter, "/var/lib/flightctl/helm/charts", testBackoffConfig)
			got, err := helm.Template(context.Background(), tc.releaseName, tc.chartPath, tc.opts...)

			if tc.wantErr {
				require.Error(err)
				require.Empty(got)
				return
			}
			require.NoError(err)
			require.Equal(tc.want, got)
		})
	}
}

func TestHelm_Uninstall(t *testing.T) {
	testCases := []struct {
		name        string
		releaseName string
		opts        []HelmOption
		setupMock   func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr     bool
	}{
		{
			name:        "success without options",
			releaseName: "myapp",
			opts:        nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"uninstall", "myapp",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "success with namespace",
			releaseName: "myapp",
			opts:        []HelmOption{WithNamespace("production")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"uninstall", "myapp",
					"--namespace", "production",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "success with kubeconfig",
			releaseName: "myapp",
			opts:        []HelmOption{WithKubeconfig("/tmp/kubeconfig")},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/kubeconfig").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"uninstall", "myapp",
					"--kubeconfig", "/tmp/kubeconfig",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "success with namespace and kubeconfig",
			releaseName: "myapp",
			opts: []HelmOption{
				WithNamespace("production"),
				WithKubeconfig("/tmp/kubeconfig"),
			},
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/tmp/kubeconfig").Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"uninstall", "myapp",
					"--namespace", "production",
					"--kubeconfig", "/tmp/kubeconfig",
				}).Return("", "", 0)
			},
			wantErr: false,
		},
		{
			name:        "error from helm uninstall",
			releaseName: "myapp",
			opts:        nil,
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"uninstall", "myapp",
				}).Return("", "Error: release not found", 1)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)
			helm := NewHelm(log, mockExec, mockReadWriter, "/var/lib/flightctl/helm/charts", testBackoffConfig)
			err := helm.Uninstall(context.Background(), tc.releaseName, tc.opts...)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}
