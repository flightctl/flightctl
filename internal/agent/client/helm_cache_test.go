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

func TestHelmChartCache_ChartDir(t *testing.T) {
	testCases := []struct {
		name      string
		chartsDir string
		chartRef  string
		want      string
	}{
		{
			name:      "OCI chart reference",
			chartsDir: "/var/lib/flightctl/helm/charts",
			chartRef:  "oci://registry.example.com/charts/myapp:1.0.0",
			want:      "/var/lib/flightctl/helm/charts/registry.example.com_charts_myapp_1.0.0",
		},
		{
			name:      "chart with complex path",
			chartsDir: "/var/lib/flightctl/helm/charts",
			chartRef:  "oci://quay.io/flightctl/my-web-app:2.1.3",
			want:      "/var/lib/flightctl/helm/charts/quay.io_flightctl_my-web-app_2.1.3",
		},
		{
			name:      "chart with prerelease version",
			chartsDir: "/data/charts",
			chartRef:  "oci://registry.io/webapp:1.0.0-rc1",
			want:      "/data/charts/registry.io_webapp_1.0.0-rc1",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			cache := newHelmChartCache(nil, tc.chartsDir, mockReadWriter, log)
			got := cache.ChartDir(tc.chartRef)

			require.Equal(t, tc.want, got)
		})
	}
}

func TestHelmChartCache_IsChartResolved(t *testing.T) {
	testCases := []struct {
		name      string
		chartDir  string
		setupMock func(*fileio.MockReadWriter)
		want      bool
		wantErr   bool
	}{
		{
			name:     "chart is resolved",
			chartDir: "/var/lib/flightctl/helm/charts/myapp-1.0.0",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0/.flightctl-chart-ready", gomock.Any()).
					Return(true, nil)
			},
			want:    true,
			wantErr: false,
		},
		{
			name:     "chart is not resolved",
			chartDir: "/var/lib/flightctl/helm/charts/myapp-1.0.0",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0/.flightctl-chart-ready", gomock.Any()).
					Return(false, nil)
			},
			want:    false,
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockReadWriter)
			cache := newHelmChartCache(nil, "/var/lib/flightctl/helm/charts", mockReadWriter, log)
			got, err := cache.IsChartResolved(tc.chartDir)

			if tc.wantErr {
				require.Error(err)
				return
			}

			require.NoError(err)
			require.Equal(tc.want, got)
		})
	}
}

func TestHelmChartCache_MarkChartResolved(t *testing.T) {
	testCases := []struct {
		name      string
		chartDir  string
		setupMock func(*fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name:     "success",
			chartDir: "/var/lib/flightctl/helm/charts/myapp-1.0.0",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().WriteFile(
					"/var/lib/flightctl/helm/charts/myapp-1.0.0/.flightctl-chart-ready",
					[]byte{},
					fileio.DefaultFilePermissions,
				).Return(nil)
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
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockReadWriter)
			cache := newHelmChartCache(nil, "/var/lib/flightctl/helm/charts", mockReadWriter, log)
			err := cache.MarkChartResolved(tc.chartDir)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelmChartCache_ChartExists(t *testing.T) {
	testCases := []struct {
		name      string
		chartDir  string
		setupMock func(*fileio.MockReadWriter)
		want      bool
		wantErr   bool
	}{
		{
			name:     "chart exists with Chart.yaml",
			chartDir: "/var/lib/flightctl/helm/charts/myapp-1.0.0",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0").
					Return(true, nil)
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0/Chart.yaml").
					Return(true, nil)
			},
			want:    true,
			wantErr: false,
		},
		{
			name:     "directory exists but no Chart.yaml",
			chartDir: "/var/lib/flightctl/helm/charts/myapp-1.0.0",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0").
					Return(true, nil)
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0/Chart.yaml").
					Return(false, nil)
			},
			want:    false,
			wantErr: false,
		},
		{
			name:     "directory does not exist",
			chartDir: "/var/lib/flightctl/helm/charts/myapp-1.0.0",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0").
					Return(false, nil)
			},
			want:    false,
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			log := log.NewPrefixLogger("test")
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockReadWriter)
			cache := newHelmChartCache(nil, "/var/lib/flightctl/helm/charts", mockReadWriter, log)
			got, err := cache.ChartExists(tc.chartDir)

			if tc.wantErr {
				require.Error(err)
				return
			}

			require.NoError(err)
			require.Equal(tc.want, got)
		})
	}
}

func TestHelmChartCache_RemoveChart(t *testing.T) {
	testCases := []struct {
		name      string
		chartDir  string
		setupMock func(*fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name:     "success - chart exists",
			chartDir: "/var/lib/flightctl/helm/charts/myapp-1.0.0",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0").
					Return(true, nil)
				mockRW.EXPECT().RemoveAll("/var/lib/flightctl/helm/charts/myapp-1.0.0").
					Return(nil)
			},
			wantErr: false,
		},
		{
			name:     "success - chart does not exist",
			chartDir: "/var/lib/flightctl/helm/charts/myapp-1.0.0",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts/myapp-1.0.0").
					Return(false, nil)
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
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockReadWriter)
			cache := newHelmChartCache(nil, "/var/lib/flightctl/helm/charts", mockReadWriter, log)
			err := cache.RemoveChart(tc.chartDir)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelmChartCache_EnsureChartsDir(t *testing.T) {
	testCases := []struct {
		name      string
		chartsDir string
		setupMock func(*fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name:      "directory already exists",
			chartsDir: "/var/lib/flightctl/helm/charts",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts").
					Return(true, nil)
			},
			wantErr: false,
		},
		{
			name:      "directory does not exist - creates it",
			chartsDir: "/var/lib/flightctl/helm/charts",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts").
					Return(false, nil)
				mockRW.EXPECT().MkdirAll("/var/lib/flightctl/helm/charts", fileio.DefaultDirectoryPermissions).
					Return(nil)
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
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockReadWriter)
			cache := newHelmChartCache(nil, tc.chartsDir, mockReadWriter, log)
			err := cache.EnsureChartsDir()

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelmChartCache_ResolveChart(t *testing.T) {
	chartRef := "oci://registry.example.com/charts/myapp:1.0.0"
	chartDir := "/var/lib/flightctl/helm/charts/registry.example.com_charts_myapp_1.0.0"

	testCases := []struct {
		name      string
		setupMock func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name: "already resolved - returns immediately",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(chartDir+"/.flightctl-chart-ready", gomock.Any()).
					Return(true, nil)
			},
			wantErr: false,
		},
		{
			name: "chart exists but not resolved - runs dependency update only",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(chartDir+"/.flightctl-chart-ready", gomock.Any()).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(true, nil)
				mockRW.EXPECT().PathExists(chartDir+"/Chart.yaml").
					Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", chartDir,
				}).Return("", "", 0)
				mockRW.EXPECT().WriteFile(
					chartDir+"/.flightctl-chart-ready",
					[]byte{},
					fileio.DefaultFilePermissions,
				).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "chart does not exist - pulls, renames, then updates dependencies",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(chartDir+"/.flightctl-chart-ready", gomock.Any()).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(false, nil)
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts").
					Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"pull", "oci://registry.example.com/charts/myapp",
					"--untar", "--destination", "/var/lib/flightctl/helm/charts",
					"--version", "1.0.0",
				}).Return("", "", 0)
				mockRW.EXPECT().Rename(
					"/var/lib/flightctl/helm/charts/myapp",
					chartDir,
				).Return(nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", chartDir,
				}).Return("", "", 0)
				mockRW.EXPECT().WriteFile(
					chartDir+"/.flightctl-chart-ready",
					[]byte{},
					fileio.DefaultFilePermissions,
				).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "chart does not exist and charts dir needs creation",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(chartDir+"/.flightctl-chart-ready", gomock.Any()).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(false, nil)
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts").
					Return(false, nil)
				mockRW.EXPECT().MkdirAll("/var/lib/flightctl/helm/charts", fileio.DefaultDirectoryPermissions).
					Return(nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"pull", "oci://registry.example.com/charts/myapp",
					"--untar", "--destination", "/var/lib/flightctl/helm/charts",
					"--version", "1.0.0",
				}).Return("", "", 0)
				mockRW.EXPECT().Rename(
					"/var/lib/flightctl/helm/charts/myapp",
					chartDir,
				).Return(nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", chartDir,
				}).Return("", "", 0)
				mockRW.EXPECT().WriteFile(
					chartDir+"/.flightctl-chart-ready",
					[]byte{},
					fileio.DefaultFilePermissions,
				).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "pull fails - returns error",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(chartDir+"/.flightctl-chart-ready", gomock.Any()).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(false, nil)
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts").
					Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"pull", "oci://registry.example.com/charts/myapp",
					"--untar", "--destination", "/var/lib/flightctl/helm/charts",
					"--version", "1.0.0",
				}).Return("", "Error: chart not found", 1)
			},
			wantErr: true,
		},
		{
			name: "dependency update fails - returns error",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(chartDir+"/.flightctl-chart-ready", gomock.Any()).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(true, nil)
				mockRW.EXPECT().PathExists(chartDir+"/Chart.yaml").
					Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", chartDir,
				}).Return("", "Error: repository not found", 1)
			},
			wantErr: true,
		},
		{
			name: "stale chart dir exists without Chart.yaml - removes stale dir and re-pulls",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(chartDir+"/.flightctl-chart-ready", gomock.Any()).
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(true, nil)
				mockRW.EXPECT().PathExists(chartDir+"/Chart.yaml").
					Return(false, nil)
				mockRW.EXPECT().PathExists(chartDir).
					Return(true, nil)
				mockRW.EXPECT().RemoveAll(chartDir).
					Return(nil)
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/charts").
					Return(true, nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"pull", "oci://registry.example.com/charts/myapp",
					"--untar", "--destination", "/var/lib/flightctl/helm/charts",
					"--version", "1.0.0",
				}).Return("", "", 0)
				mockRW.EXPECT().Rename(
					"/var/lib/flightctl/helm/charts/myapp",
					chartDir,
				).Return(nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"dependency", "update", chartDir,
				}).Return("", "", 0)
				mockRW.EXPECT().WriteFile(
					chartDir+"/.flightctl-chart-ready",
					[]byte{},
					fileio.DefaultFilePermissions,
				).Return(nil)
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

			helm := NewHelm(log, mockExec, mockReadWriter, "/var/lib/flightctl/helm/charts")
			cache := newHelmChartCache(helm, "/var/lib/flightctl/helm/charts", mockReadWriter, log)
			err := cache.ResolveChart(context.Background(), chartRef, chartDir)

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestParseChartRef(t *testing.T) {
	testCases := []struct {
		name        string
		chartRef    string
		wantName    string
		wantVersion string
		wantErr     bool
	}{
		{
			name:        "tag-based OCI reference",
			chartRef:    "oci://registry.example.com/charts/myapp:1.0.0",
			wantName:    "myapp",
			wantVersion: "1.0.0",
			wantErr:     false,
		},
		{
			name:        "tag-based reference with complex path",
			chartRef:    "oci://quay.io/flightctl/helm-charts/webapp:2.1.3",
			wantName:    "webapp",
			wantVersion: "2.1.3",
			wantErr:     false,
		},
		{
			name:        "digest-based OCI reference",
			chartRef:    "oci://registry.example.com/charts/myapp@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantName:    "myapp",
			wantVersion: "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantErr:     false,
		},
		{
			name:        "reference without version or digest",
			chartRef:    "oci://registry.example.com/charts/myapp",
			wantName:    "",
			wantVersion: "",
			wantErr:     true,
		},
		{
			name:        "reference with both tag and digest",
			chartRef:    "oci://registry.example.com/charts/myapp:1.0.0@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantName:    "myapp",
			wantVersion: "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantErr:     false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			name, version, err := ParseChartRef(tc.chartRef)

			if tc.wantErr {
				require.Error(err)
				return
			}

			require.NoError(err)
			require.Equal(tc.wantName, name)
			require.Equal(tc.wantVersion, version)
		})
	}
}

func TestSplitChartRef(t *testing.T) {
	testCases := []struct {
		name        string
		chartRef    string
		wantPath    string
		wantVersion string
	}{
		{
			name:        "tag-based OCI reference",
			chartRef:    "oci://registry.example.com/charts/myapp:1.0.0",
			wantPath:    "oci://registry.example.com/charts/myapp",
			wantVersion: "1.0.0",
		},
		{
			name:        "digest-based OCI reference returns full ref with empty version",
			chartRef:    "oci://registry.example.com/charts/myapp@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantPath:    "oci://registry.example.com/charts/myapp@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			wantVersion: "",
		},
		{
			name:        "reference without version or digest",
			chartRef:    "oci://registry.example.com/charts/myapp",
			wantPath:    "oci://registry.example.com/charts/myapp",
			wantVersion: "",
		},
		{
			name:        "non-OCI reference with tag",
			chartRef:    "registry.example.com/charts/myapp:1.0.0",
			wantPath:    "registry.example.com/charts/myapp",
			wantVersion: "1.0.0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			path, version := SplitChartRef(tc.chartRef)

			require.Equal(tc.wantPath, path)
			require.Equal(tc.wantVersion, version)
		})
	}
}

func TestSanitizeChartRef(t *testing.T) {
	testCases := []struct {
		name     string
		chartRef string
		want     string
	}{
		{
			name:     "tag-based OCI reference",
			chartRef: "oci://registry.example.com/charts/myapp:1.0.0",
			want:     "registry.example.com_charts_myapp_1.0.0",
		},
		{
			name:     "digest-based OCI reference",
			chartRef: "oci://registry.example.com/charts/myapp@sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
			want:     "registry.example.com_charts_myapp_sha256_a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			result := SanitizeChartRef(tc.chartRef)

			require.Equal(tc.want, result)
		})
	}
}
