package config

import (
	"context"
	"testing"

	"github.com/coreos/ignition/v2/config/shared/errors"
	ignv3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSync(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name    string
		current *v1alpha1.RenderedDeviceSpec
		desired *v1alpha1.RenderedDeviceSpec
		wantErr error
		// files which are created via the sync operation
		createdFiles []string
		// files which are removed via the sync operation
		removedFiles []string
	}{
		{
			name:    "no desired config",
			current: &v1alpha1.RenderedDeviceSpec{},
			desired: &v1alpha1.RenderedDeviceSpec{},
		},
		{
			name: "desired config is invalid",
			current: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(`{"ignition":{"version":"3.4.0"}}`),
			},
			desired: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr("invalid"),
			},
			wantErr: errors.ErrInvalid,
		},
		{
			name: "desired config is valid current is nil",
			current: &v1alpha1.RenderedDeviceSpec{
				Config: nil,
			},
			desired: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(`{"ignition":{"version":"3.4.0"}}`),
			},
		},
		{
			name: "current config is valid desired is nil",
			current: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(ignitionConfigCurrent),
			},
			desired: &v1alpha1.RenderedDeviceSpec{},
			removedFiles: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
				"/etc/example/file3.txt",
			},
		},
		{
			name: "validate removal of files",
			current: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(ignitionConfigCurrent),
			},
			desired: &v1alpha1.RenderedDeviceSpec{
				Config: util.StrToPtr(ignitionConfigDesired),
			},
			createdFiles: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
			},
			removedFiles: []string{
				"/etc/example/file3.txt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockHookManager := hook.NewMockManager(ctrl)
			mockWriter := fileio.NewMockWriter(ctrl)
			mockManagedFile := fileio.NewMockManagedFile(ctrl)
			controller := NewController(
				mockHookManager,
				mockWriter,
				log.NewPrefixLogger("test"),
			)

			for _, f := range tt.createdFiles {
				expectCreateFile(ctx, mockWriter, mockManagedFile, mockHookManager, f)
			}

			for _, f := range tt.removedFiles {
				expectRemoveFile(ctx, mockWriter, mockHookManager, f)
			}

			err := controller.Sync(ctx, tt.current, tt.desired)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
		})
	}
}

func TestComputeRemoval(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		current  []ignv3types.File
		desired  []ignv3types.File
		expected []string
	}{
		{
			name: "no desired files",
			current: []ignv3types.File{
				{Node: ignv3types.Node{Path: "/etc/example/file1.txt"}},
				{Node: ignv3types.Node{Path: "/etc/example/file2.txt"}},
			},
			desired: []ignv3types.File{},
			expected: []string{
				"/etc/example/file1.txt",
				"/etc/example/file2.txt",
			},
		},
		{
			name:    "no current files",
			current: []ignv3types.File{},
			desired: []ignv3types.File{
				{Node: ignv3types.Node{Path: "/etc/example/file1.txt"}},
				{Node: ignv3types.Node{Path: "/etc/example/file2.txt"}},
			},
			expected: []string{},
		},
		{
			name: "remove diff",
			current: []ignv3types.File{
				{Node: ignv3types.Node{Path: "/etc/example/file1.txt"}},
				{Node: ignv3types.Node{Path: "/etc/example/file2.txt"}},
				{Node: ignv3types.Node{Path: "/etc/example/file3.txt"}},
			},
			desired: []ignv3types.File{
				{Node: ignv3types.Node{Path: "/etc/example/file1.txt"}},
				{Node: ignv3types.Node{Path: "/etc/example/file3.txt"}},
			},
			expected: []string{
				"/etc/example/file2.txt",
			},
		},
		{
			name:     "no files",
			current:  []ignv3types.File{},
			desired:  []ignv3types.File{},
			expected: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := computeRemoval(tt.current, tt.desired)
			require.Equal(tt.expected, actual)
		})
	}
}

func expectCreateFile(ctx context.Context, mockWriter *fileio.MockWriter, mockManagedFile *fileio.MockManagedFile, mockHookManager *hook.MockManager, f string) {
	mockWriter.EXPECT().CreateManagedFile(gomock.Any()).Return(mockManagedFile)
	mockManagedFile.EXPECT().IsUpToDate().Return(false, nil)
	mockManagedFile.EXPECT().Exists().Return(false, nil)
	mockManagedFile.EXPECT().Write().Return(nil)
	mockHookManager.EXPECT().OnBeforeCreate(ctx, f)
	mockHookManager.EXPECT().OnAfterCreate(ctx, f)
}

func expectRemoveFile(ctx context.Context, mockWriter *fileio.MockWriter, mockHookManager *hook.MockManager, f string) {
	mockHookManager.EXPECT().OnBeforeRemove(ctx, f)
	mockHookManager.EXPECT().OnAfterRemove(ctx, f)
	mockWriter.EXPECT().RemoveFile(f).Return(nil)
}

var ignitionConfigCurrent = `{
  "ignition": {
    "version": "3.1.0"
  },
  "storage": {
    "files": [
      {
        "path": "/etc/example/file1.txt",
        "contents": {
          "source": "data:,File%201%20contents"
        },
        "mode": 420
      },
      {
        "path": "/etc/example/file2.txt",
        "contents": {
          "source": "data:,File%202%20contents"
        },
        "mode": 420
      },
      {
        "path": "/etc/example/file3.txt",
        "contents": {
          "source": "data:,File%203%20contents"
        },
        "mode": 420
      }
    ]
  }
}`

var ignitionConfigDesired = `{
  "ignition": {
    "version": "3.1.0"
  },
  "storage": {
    "files": [
      {
        "path": "/etc/example/file1.txt",
        "contents": {
          "source": "data:,File%201%20contents"
        },
        "mode": 420
      },
      {
        "path": "/etc/example/file2.txt",
        "contents": {
          "source": "data:,File%202%20contents"
        },
        "mode": 420
      }
    ]
  }
}`
