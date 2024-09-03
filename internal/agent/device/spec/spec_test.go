package spec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestBootstrapCheckRollback(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockBootcClient := container.NewMockBootcClient(ctrl)

	s := &SpecManager{
		log:              log.NewPrefixLogger("test"),
		deviceReadWriter: mockReadWriter,
		bootcClient:      mockBootcClient,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("no rollback: bootstrap case empty desired spec", func(t *testing.T) {
		wantIsRollback := false
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return([]byte(`{}`), nil)

		isRollback, err := s.IsRollingBack(ctx)
		require.NoError(err)
		require.Equal(wantIsRollback, isRollback)
	})

	t.Run("no rollback: booted os is equal to desired", func(t *testing.T) {
		wantIsRollback := false
		rollbackImage := "flightctl-device:v1"
		bootedImage := "flightctl-device:v2"
		desiredImage := "flightctl-device:v2"

		// desiredSpec
		desiredSpec, err := createTestSpec(desiredImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(desiredSpec, nil)

		// rollbackSpec
		rollbackSpec, err := createTestSpec(rollbackImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(rollbackSpec, nil)

		// bootcStatus
		bootcStatus := &container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = bootedImage
		mockBootcClient.EXPECT().Status(ctx).Return(bootcStatus, nil)

		isRollback, err := s.IsRollingBack(ctx)
		require.NoError(err)
		require.Equal(wantIsRollback, isRollback)
	})

	t.Run("rollback case: rollback os equal to booted os but not desired", func(t *testing.T) {
		wantIsRollback := true
		rollbackImage := "flightctl-device:v1"
		bootedImage := "flightctl-device:v1"
		desiredImage := "flightctl-device:v2"

		// desiredSpec
		desiredSpec, err := createTestSpec(desiredImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(desiredSpec, nil)

		// rollbackSpec
		rollbackSpec, err := createTestSpec(rollbackImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(rollbackSpec, nil)

		// bootcStatus
		bootcStatus := &container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = bootedImage
		mockBootcClient.EXPECT().Status(ctx).Return(bootcStatus, nil)

		isRollback, err := s.IsRollingBack(ctx)
		require.NoError(err)
		require.Equal(wantIsRollback, isRollback)
	})

}

func TestNewManager(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockBootcClient := container.NewMockBootcClient(ctrl)
	logger := log.NewPrefixLogger("test")
	backoff := wait.Backoff{}

	t.Run("constructs file paths for the spec files", func(t *testing.T) {
		manager := NewManager(
			"device-name",
			"test/directory/structure/",
			mockReadWriter,
			mockBootcClient,
			backoff,
			logger,
		)

		require.Equal("test/directory/structure/current.json", manager.currentPath)
		require.Equal("test/directory/structure/desired.json", manager.desiredPath)
		require.Equal("test/directory/structure/rollback.json", manager.rollbackPath)
	})
}

func TestInitialize(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	s := &SpecManager{
		deviceReadWriter: mockReadWriter,
	}

	writeErr := errors.New("write failure")

	t.Run("error writing current file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)
		err := s.Initialize()

		require.ErrorIs(err, ErrWritingSpec)
	})

	t.Run("error writing desired file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// desired
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)

		err := s.Initialize()
		require.ErrorIs(err, ErrWritingSpec)
	})

	t.Run("error writing rollback file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// desired
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// rollback
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)

		err := s.Initialize()
		require.ErrorIs(err, ErrWritingSpec)
	})

	t.Run("successful initialization", func(t *testing.T) {
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Times(3).Return(nil)
		err := s.Initialize()
		require.NoError(err)
	})
}

func TestEnsure(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	s := &SpecManager{
		log:              log.NewPrefixLogger("test"),
		deviceReadWriter: mockReadWriter,
	}

	fileErr := errors.New("invalid file")

	t.Run("error checking if file exists", func(t *testing.T) {
		mockReadWriter.EXPECT().FileExists(gomock.Any()).Return(false, fileErr)
		err := s.Ensure()
		require.ErrorIs(err, ErrCheckingFileExists)
	})

	t.Run("error writing file when it does not exist", func(t *testing.T) {
		mockReadWriter.EXPECT().FileExists(gomock.Any()).Return(false, nil)
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(fileErr)
		err := s.Ensure()
		require.ErrorIs(err, ErrWritingSpec)
	})

	t.Run("files are written when they don't exist", func(t *testing.T) {
		// First two files checked exist
		mockReadWriter.EXPECT().FileExists(gomock.Any()).Times(2).Return(true, nil)
		// Third file does not exist, so then expect a write
		mockReadWriter.EXPECT().FileExists(gomock.Any()).Times(1).Return(false, nil)
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Times(1).Return(nil)
		err := s.Ensure()
		require.NoError(err)
	})

	t.Run("no files are written when they all exist", func(t *testing.T) {
		mockReadWriter.EXPECT().FileExists(gomock.Any()).Times(3).Return(true, nil)
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
		err := s.Ensure()
		require.NoError(err)
	})
}

func createTestSpec(image string) ([]byte, error) {
	spec := v1alpha1.RenderedDeviceSpec{
		Os: &v1alpha1.DeviceOSSpec{
			Image: image,
		},
	}
	return json.Marshal(spec)
}
