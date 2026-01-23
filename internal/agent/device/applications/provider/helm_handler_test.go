package provider

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestGetHelmProviderValuesPath(t *testing.T) {
	tests := []struct {
		name     string
		appName  string
		expected string
	}{
		{
			name:     "simple app name",
			appName:  "myapp",
			expected: "/var/lib/flightctl/helm/values/myapp/flightctl-values.yaml",
		},
		{
			name:     "app name with dashes",
			appName:  "my-helm-app",
			expected: "/var/lib/flightctl/helm/values/my-helm-app/flightctl-values.yaml",
		},
		{
			name:     "empty app name",
			appName:  "",
			expected: "/var/lib/flightctl/helm/values/flightctl-values.yaml",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			result := GetHelmProviderValuesPath(tc.appName)
			require.Equal(tc.expected, result)
		})
	}
}

func TestHelmHandler_ID(t *testing.T) {
	tests := []struct {
		name      string
		appName   string
		namespace string
		expected  string
	}{
		{
			name:      "with namespace",
			appName:   "myapp",
			namespace: "production",
			expected:  "production_myapp",
		},
		{
			name:      "empty namespace",
			appName:   "myapp",
			namespace: "",
			expected:  "flightctl-myapp_myapp",
		},
		{
			name:      "both set",
			appName:   "nginx",
			namespace: "default",
			expected:  "default_nginx",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			handler := &helmHandler{
				name: tc.appName,
				spec: &v1beta1.ImageApplicationProviderSpec{
					Namespace: lo.ToPtr(tc.namespace),
				},
			}

			result := handler.ID()
			require.Equal(tc.expected, result)
		})
	}
}

func TestHelmHandler_ProviderValuesPath(t *testing.T) {
	tests := []struct {
		name     string
		appName  string
		values   map[string]interface{}
		expected string
	}{
		{
			name:     "with values returns path",
			appName:  "myapp",
			values:   map[string]interface{}{"key": "value"},
			expected: "/var/lib/flightctl/helm/values/myapp/flightctl-values.yaml",
		},
		{
			name:     "empty values returns empty string",
			appName:  "myapp",
			values:   map[string]interface{}{},
			expected: "",
		},
		{
			name:     "nil values returns empty string",
			appName:  "myapp",
			values:   nil,
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			var valuesPtr *map[string]interface{}
			if tc.values != nil {
				valuesPtr = &tc.values
			}

			handler := &helmHandler{
				name: tc.appName,
				spec: &v1beta1.ImageApplicationProviderSpec{
					Values: valuesPtr,
				},
			}

			result := handler.ProviderValuesPath()
			require.Equal(tc.expected, result)
		})
	}
}

func TestHelmHandler_Install(t *testing.T) {
	tests := []struct {
		name      string
		appName   string
		values    map[string]interface{}
		setupMock func(*fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name:    "no values skips install",
			appName: "myapp",
			values:  nil,
			setupMock: func(mockRW *fileio.MockReadWriter) {
			},
			wantErr: false,
		},
		{
			name:    "empty values skips install",
			appName: "myapp",
			values:  map[string]interface{}{},
			setupMock: func(mockRW *fileio.MockReadWriter) {
			},
			wantErr: false,
		},
		{
			name:    "with values writes file",
			appName: "myapp",
			values:  map[string]interface{}{"replicaCount": 3},
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().MkdirAll("/var/lib/flightctl/helm/values/myapp", fileio.DefaultDirectoryPermissions).Return(nil)
				mockRW.EXPECT().WriteFile(
					"/var/lib/flightctl/helm/values/myapp/flightctl-values.yaml",
					gomock.Any(),
					fileio.DefaultFilePermissions,
				).Return(nil)
			},
			wantErr: false,
		},
		{
			name:    "mkdir fails returns error",
			appName: "myapp",
			values:  map[string]interface{}{"key": "value"},
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().MkdirAll("/var/lib/flightctl/helm/values/myapp", fileio.DefaultDirectoryPermissions).Return(
					filepath.ErrBadPattern,
				)
			},
			wantErr: true,
		},
		{
			name:    "write file fails returns error",
			appName: "myapp",
			values:  map[string]interface{}{"key": "value"},
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().MkdirAll("/var/lib/flightctl/helm/values/myapp", fileio.DefaultDirectoryPermissions).Return(nil)
				mockRW.EXPECT().WriteFile(
					"/var/lib/flightctl/helm/values/myapp/flightctl-values.yaml",
					gomock.Any(),
					fileio.DefaultFilePermissions,
				).Return(filepath.ErrBadPattern)
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMock(mockRW)

			var valuesPtr *map[string]interface{}
			if tc.values != nil {
				valuesPtr = &tc.values
			}

			handler := &helmHandler{
				name: tc.appName,
				spec: &v1beta1.ImageApplicationProviderSpec{
					Values: valuesPtr,
				},
				rw:  mockRW,
				log: log.NewPrefixLogger("test"),
			}

			err := handler.Install(context.Background())
			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelmHandler_Remove(t *testing.T) {
	tests := []struct {
		name      string
		appName   string
		setupMock func(*fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name:    "values directory exists and is removed",
			appName: "myapp",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/values/myapp").Return(true, nil)
				mockRW.EXPECT().RemoveAll("/var/lib/flightctl/helm/values/myapp").Return(nil)
			},
			wantErr: false,
		},
		{
			name:    "values directory does not exist",
			appName: "myapp",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/values/myapp").Return(false, nil)
			},
			wantErr: false,
		},
		{
			name:    "path exists check fails",
			appName: "myapp",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/values/myapp").Return(false, filepath.ErrBadPattern)
			},
			wantErr: true,
		},
		{
			name:    "remove fails returns error",
			appName: "myapp",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/flightctl/helm/values/myapp").Return(true, nil)
				mockRW.EXPECT().RemoveAll("/var/lib/flightctl/helm/values/myapp").Return(filepath.ErrBadPattern)
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMock(mockRW)

			handler := &helmHandler{
				name: tc.appName,
				spec: &v1beta1.ImageApplicationProviderSpec{},
				rw:   mockRW,
				log:  log.NewPrefixLogger("test"),
			}

			err := handler.Remove(context.Background())
			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelmHandler_Volumes(t *testing.T) {
	require := require.New(t)

	handler := &helmHandler{
		name: "myapp",
		spec: &v1beta1.ImageApplicationProviderSpec{},
	}

	volumes, err := handler.Volumes()
	require.NoError(err)
	require.Nil(volumes)
}

func TestWriteFlightctlHelmValues(t *testing.T) {
	tests := []struct {
		name      string
		values    *map[string]any
		setupMock func(*fileio.MockReadWriter)
		wantErr   bool
	}{
		{
			name:   "writes values successfully",
			values: &map[string]any{"key": "value", "count": 5},
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().WriteFile(
					"/test/dir/flightctl-values.yaml",
					gomock.Any(),
					fileio.DefaultFilePermissions,
				).Return(nil)
			},
			wantErr: false,
		},
		{
			name:      "nil values does nothing",
			values:    nil,
			setupMock: func(mockRW *fileio.MockReadWriter) {},
			wantErr:   false,
		},
		{
			name:      "empty values does nothing",
			values:    &map[string]any{},
			setupMock: func(mockRW *fileio.MockReadWriter) {},
			wantErr:   false,
		},
		{
			name:   "write error returns error",
			values: &map[string]any{"key": "value"},
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().WriteFile(
					"/test/dir/flightctl-values.yaml",
					gomock.Any(),
					fileio.DefaultFilePermissions,
				).Return(filepath.ErrBadPattern)
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRW := fileio.NewMockReadWriter(ctrl)
			tc.setupMock(mockRW)

			err := writeFlightctlHelmValues(context.Background(), tc.values, "/test/dir", mockRW)
			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}
