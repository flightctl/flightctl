package spec

import (
	"context"
	"net/http"
	"os"
	"testing"

	v1alpha1 "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestManager(t *testing.T) {
	tests := []struct {
		name                    string
		getRenderedResponseCode int
		desiredRenderedVersion  string
		wantErr                 error
		wantReadErr             error
		wantInitialize          bool
	}{
		{
			name:           "happy path",
			wantInitialize: true,
		},
		{
			name:                    "error getting rendered spec during runtime",
			wantReadErr:             ErrMissingRenderedSpec,
			getRenderedResponseCode: http.StatusOK,
			desiredRenderedVersion:  "1",
			wantInitialize:          false,
		},
		{
			name:                    "skip sync 204 from api",
			getRenderedResponseCode: http.StatusNoContent,
			wantInitialize:          true,
			wantErr:                 ErrNoContent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			tmpDir := t.TempDir()
			err := os.MkdirAll(tmpDir+"/etc/flightctl", 0755)
			require.NoError(err)
			backoff := wait.Backoff{
				Steps:    1,
				Duration: 1,
				Factor:   1,
				Jitter:   0,
			}

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockManagementClient := client.NewMockManagement(ctrl)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			log := log.NewPrefixLogger("")
			readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tmpDir))

			manager := NewManager(
				"testDeviceName",
				tmpDir,
				readWriter,
				backoff,
				log,
			)

			// initialize writes empty spec files to disk for current, desired and rollback
			if tt.wantInitialize {
				err := manager.Initialize()
				require.NoError(err)
			}
			_, err = manager.Read(Current)
			if tt.wantReadErr != nil {
				require.ErrorIs(err, tt.wantReadErr)
				return
			}
			require.NoError(err)

			mockManagementClient.EXPECT().GetRenderedDeviceSpec(gomock.Any(), gomock.Any(), gomock.Any()).
				Return(&v1alpha1.RenderedDeviceSpec{RenderedVersion: tt.desiredRenderedVersion}, tt.getRenderedResponseCode, nil).Times(1)

			manager.SetClient(mockManagementClient)
			isRolledBack := false
			desired, err := manager.GetDesired(ctx, "1", isRolledBack)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
			require.Equal(tt.desiredRenderedVersion, desired.RenderedVersion)
		})
	}

}
