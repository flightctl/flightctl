package config

import (
	"context"
	"testing"

	"github.com/coreos/ignition/v2/config/shared/errors"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSync(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name         string
		current      *v1alpha1.RenderedDeviceSpec
		desired      *v1alpha1.RenderedDeviceSpec
		wantErr      error
		createdFiles []string
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
				"/etc/example/file3.txt",
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
			mockHookManager := hook.NewMockManager(ctrl)
			mockWriter := fileio.NewMockWriter(ctrl)
			mockManagedFile := fileio.NewMockManagedFile(ctrl)
			controller := NewController(
				mockHookManager,
				mockWriter,
				log.NewPrefixLogger("test"),
			)
			mockHookManager.EXPECT().Sync(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			for _, f := range tt.createdFiles {
				mockWriter.EXPECT().CreateManagedFile(gomock.Any()).Return(mockManagedFile)
				mockManagedFile.EXPECT().IsUpToDate().Return(false, nil)
				mockManagedFile.EXPECT().Exists().Return(false, nil)
				mockManagedFile.EXPECT().Write().Return(nil)
				mockHookManager.EXPECT().OnBeforeCreate(gomock.Any(), f)
				mockHookManager.EXPECT().OnAfterCreate(gomock.Any(), f)
			}

			for i, f := range lo.Without(tt.createdFiles, tt.removedFiles...) {
				mockWriter.EXPECT().CreateManagedFile(gomock.Any()).Return(mockManagedFile)
				upToDate := i%2 == 0
				mockManagedFile.EXPECT().IsUpToDate().Return(upToDate, nil)
				if !upToDate {
					mockManagedFile.EXPECT().Exists().Return(true, nil)
					mockManagedFile.EXPECT().Write().Return(nil)
					mockHookManager.EXPECT().OnBeforeUpdate(gomock.Any(), f)
					mockHookManager.EXPECT().OnAfterUpdate(gomock.Any(), f)
				}
			}

			for _, f := range tt.removedFiles {
				mockHookManager.EXPECT().OnBeforeRemove(gomock.Any(), f)
				mockHookManager.EXPECT().OnAfterRemove(gomock.Any(), f)
				mockWriter.EXPECT().RemoveFile(f).Return(nil)
			}
			// Write the current config to the disk
			if tt.current.Config != nil {
				currentConfigRaw := []byte(*tt.current.Config)
				currentIgnitionConfig, err := ParseAndConvertConfig(currentConfigRaw)
				require.NoError(err)
				err = controller.WriteIgnitionFiles(ctx, currentIgnitionConfig.Storage.Files)
				require.NoError(err)
			}

			err := controller.Sync(ctx, tt.current, tt.desired)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			ctrl.Finish()
		})
	}
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
