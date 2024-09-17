package spec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
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

		require.ErrorIs(err, ErrWritingRenderedSpec)
	})

	t.Run("error writing desired file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// desired
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)

		err := s.Initialize()
		require.ErrorIs(err, ErrWritingRenderedSpec)
	})

	t.Run("error writing rollback file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// desired
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// rollback
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)

		err := s.Initialize()
		require.ErrorIs(err, ErrWritingRenderedSpec)
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
		require.ErrorIs(err, ErrWritingRenderedSpec)
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

func TestRead(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	s := &SpecManager{
		log:              log.NewPrefixLogger("test"),
		deviceReadWriter: mockReadWriter,
	}

	t.Run("ensure proper error handling on read failure", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(nil, errors.New("read gone wrong"))
		_, err := s.Read(Current)
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("reads a device spec", func(t *testing.T) {
		image := "flightctl-device:v1"
		spec, err := createTestSpec(image)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(spec, nil)

		specFromRead, err := s.Read(Current)
		require.NoError(err)
		require.Equal(image, specFromRead.Os.Image)
	})
}

func Test_readRenderedSpecFromFile(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReader := fileio.NewMockReader(ctrl)
	filePath := "test/path/spec.json"

	t.Run("error when the file does not exist", func(t *testing.T) {
		mockReader.EXPECT().ReadFile(filePath).Return(nil, os.ErrNotExist)

		_, err := readRenderedSpecFromFile(mockReader, filePath)
		require.ErrorIs(err, ErrMissingRenderedSpec)
	})

	t.Run("error reading file when it does exist", func(t *testing.T) {
		mockReader.EXPECT().ReadFile(filePath).Return(nil, errors.New("cannot read"))

		_, err := readRenderedSpecFromFile(mockReader, filePath)
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("error when the file is not a valid spec", func(t *testing.T) {
		invalidSpec := []byte("Not json data for a spec")
		mockReader.EXPECT().ReadFile(filePath).Return(invalidSpec, nil)

		_, err := readRenderedSpecFromFile(mockReader, filePath)
		require.ErrorIs(err, ErrUnmarshalSpec)
	})

	t.Run("returns the read spec", func(t *testing.T) {
		image := "flightctl-device:v1"
		spec, err := createTestSpec(image)
		require.NoError(err)
		mockReader.EXPECT().ReadFile(gomock.Any()).Return(spec, nil)

		specFromRead, err := readRenderedSpecFromFile(mockReader, filePath)
		require.NoError(err)
		require.Equal(image, specFromRead.Os.Image)
	})
}

func Test_writeRenderedToFile(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWriter := fileio.NewMockWriter(ctrl)
	filePath := "path/to/write"
	spec := createRenderedTestSpec("test-image")

	marshaled, err := json.Marshal(spec)
	require.NoError(err)

	t.Run("returns an error when the write fails", func(t *testing.T) {
		writeErr := errors.New("some failure")
		mockWriter.EXPECT().WriteFile(filePath, marshaled, fileio.DefaultFilePermissions).Return(writeErr)

		err = writeRenderedToFile(mockWriter, spec, filePath)
		require.ErrorIs(err, ErrWritingRenderedSpec)
	})

	t.Run("writes a rendered spec", func(t *testing.T) {
		mockWriter.EXPECT().WriteFile(filePath, marshaled, fileio.DefaultFilePermissions).Return(nil)

		err = writeRenderedToFile(mockWriter, spec, filePath)
		require.NoError(err)
	})
}

func TestUpgrade(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	desiredPath := "test/desired.json"
	currentPath := "test/current.json"
	rollbackPath := "test/rollback/json"
	s := &SpecManager{
		log:              log.NewPrefixLogger("test"),
		deviceReadWriter: mockReadWriter,
		desiredPath:      desiredPath,
		currentPath:      currentPath,
		rollbackPath:     rollbackPath,
	}

	specErr := errors.New("error with spec")

	emptySpec, err := createEmptyTestSpec()
	require.NoError(err)

	t.Run("error reading desired spec", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(nil, specErr)

		err := s.Upgrade()
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("error writing desired spec to current", func(t *testing.T) {
		desiredSpec, err := createTestSpec("flightctl-device:v2")
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)
		mockReadWriter.EXPECT().WriteFile(currentPath, desiredSpec, gomock.Any()).Return(specErr)

		err = s.Upgrade()
		require.ErrorIs(err, ErrWritingRenderedSpec)
	})

	t.Run("error writing the rollback spec", func(t *testing.T) {
		desiredSpec, err := createTestSpec("flightctl-device:v2")
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)
		mockReadWriter.EXPECT().WriteFile(currentPath, desiredSpec, gomock.Any()).Return(nil)
		mockReadWriter.EXPECT().WriteFile(rollbackPath, emptySpec, gomock.Any()).Return(specErr)

		err = s.Upgrade()
		require.ErrorIs(err, ErrWritingRenderedSpec)
	})

	t.Run("clears out the rollback spec", func(t *testing.T) {
		desiredSpec, err := createTestSpec("flightctl-device:v2")
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)
		mockReadWriter.EXPECT().WriteFile(currentPath, desiredSpec, gomock.Any()).Return(nil)
		mockReadWriter.EXPECT().WriteFile(rollbackPath, emptySpec, gomock.Any()).Return(nil)

		err = s.Upgrade()
		require.NoError(err)
	})
}

func TestIsOSUpdate(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	desiredPath := "test/desired.json"
	currentPath := "test/current.json"
	s := &SpecManager{
		log:              log.NewPrefixLogger("test"),
		deviceReadWriter: mockReadWriter,
		desiredPath:      desiredPath,
		currentPath:      currentPath,
	}

	emptySpec, err := createEmptyTestSpec()
	require.NoError(err)

	specErr := errors.New("error with spec")

	t.Run("error reading current spec", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(nil, specErr)

		_, err := s.IsOSUpdate()
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("error reading desired spec", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(emptySpec, nil)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(nil, specErr)

		_, err := s.IsOSUpdate()
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("both specs are empty", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(emptySpec, nil)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(emptySpec, nil)

		osUpdate, err := s.IsOSUpdate()
		require.NoError(err)
		require.Equal(false, osUpdate)
	})

	t.Run("current and desired os images are the same", func(t *testing.T) {
		image := "flightctl-device:v2"

		currentSpec, err := createTestSpec(image)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(currentSpec, nil)

		desiredSpec, err := createTestSpec(image)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)

		osUpdate, err := s.IsOSUpdate()
		require.NoError(err)
		require.Equal(false, osUpdate)
	})

	t.Run("current and desired os images are different", func(t *testing.T) {
		currentImage := "flightctl-device:v2"
		desiredImage := "flightctl-deivce:v3"

		currentSpec, err := createTestSpec(currentImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(currentSpec, nil)

		desiredSpec, err := createTestSpec(desiredImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)

		osUpdate, err := s.IsOSUpdate()
		require.NoError(err)
		require.Equal(true, osUpdate)
	})
}

func TestCheckOsReconciliation(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockBootcClient := container.NewMockBootcClient(ctrl)

	desiredPath := "test/desired.json"
	s := &SpecManager{
		log:              log.NewPrefixLogger("test"),
		deviceReadWriter: mockReadWriter,
		bootcClient:      mockBootcClient,
		desiredPath:      desiredPath,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emptySpec, err := json.Marshal(&v1alpha1.RenderedDeviceSpec{})
	require.NoError(err)

	t.Run("error getting bootc status", func(t *testing.T) {
		bootcErr := errors.New("bootc problem")
		mockBootcClient.EXPECT().Status(ctx).Return(nil, bootcErr)

		_, _, err := s.CheckOsReconciliation(ctx)
		require.ErrorIs(err, ErrGettingBootcStatus)
	})

	t.Run("error reading desired spec", func(t *testing.T) {
		bootcStatus := &container.BootcHost{}
		mockBootcClient.EXPECT().Status(ctx).Return(bootcStatus, nil)

		readErr := errors.New("unable to read file")
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(emptySpec, readErr)

		_, _, err = s.CheckOsReconciliation(ctx)
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("desired os is not set in the spec", func(t *testing.T) {
		bootedImage := "flightctl-device:v1"

		bootcStatus := &container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = bootedImage
		mockBootcClient.EXPECT().Status(ctx).Return(bootcStatus, nil)

		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(emptySpec, nil)

		bootedOSImage, desiredImageIsBooted, err := s.CheckOsReconciliation(ctx)
		require.NoError(err)
		require.Equal(bootedOSImage, bootedImage)
		require.Equal(false, desiredImageIsBooted)
	})

	t.Run("booted image and desired image are different", func(t *testing.T) {
		bootedImage := "flightctl-device:v1"
		desiredImage := "flightctl-device:v2"

		bootcStatus := &container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = bootedImage
		mockBootcClient.EXPECT().Status(ctx).Return(bootcStatus, nil)

		desiredSpec, err := createTestSpec(desiredImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)

		bootedOSImage, desiredImageIsBooted, err := s.CheckOsReconciliation(ctx)
		require.NoError(err)
		require.Equal(bootedOSImage, bootedImage)
		require.Equal(false, desiredImageIsBooted)
	})

	t.Run("booted image and desired image are the same", func(t *testing.T) {
		image := "flightctl-device:v2"

		bootcStatus := &container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = image
		mockBootcClient.EXPECT().Status(ctx).Return(bootcStatus, nil)

		desiredSpec, err := createTestSpec(image)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)

		bootedOSImage, desiredImageIsBooted, err := s.CheckOsReconciliation(ctx)
		require.NoError(err)
		require.Equal(bootedOSImage, image)
		require.Equal(true, desiredImageIsBooted)
	})
}

func TestPrepareRollback(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockBootcClient := container.NewMockBootcClient(ctrl)

	currentPath := "test/current.json"
	rollbackPath := "test/rollback.json"
	s := &SpecManager{
		deviceReadWriter: mockReadWriter,
		bootcClient:      mockBootcClient,
		currentPath:      currentPath,
		rollbackPath:     rollbackPath,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emptySpec, err := createEmptyTestSpec()
	require.NoError(err)

	specErr := errors.New("unable to use spec")

	t.Run("error reading current spec", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(emptySpec, specErr)

		err = s.PrepareRollback(ctx)
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("error writing rollback spec", func(t *testing.T) {
		currentImage := "flightctl-device:v1"

		currentSpec, err := createTestSpec(currentImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(currentSpec, nil)

		mockReadWriter.EXPECT().WriteFile(rollbackPath, gomock.Any(), gomock.Any()).Return(specErr)

		err = s.PrepareRollback(ctx)
		require.ErrorIs(err, ErrWritingRenderedSpec)
	})

	t.Run("writes the os image from the current spec when it is defined", func(t *testing.T) {
		currentImage := "flightctl-device:v1"

		currentSpec, err := createTestSpec(currentImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(currentSpec, nil)

		rollbackSpec, err := createTestSpec(currentImage)
		require.NoError(err)
		mockReadWriter.EXPECT().WriteFile(rollbackPath, rollbackSpec, gomock.Any()).Return(nil)

		err = s.PrepareRollback(ctx)
		require.NoError(err)
	})

	t.Run("writes the os image from bootc when the current spec os is nil", func(t *testing.T) {
		bootedImage := "flightctl-device:v1"

		renderedCurrentSpec := createRenderedTestSpec("")
		renderedCurrentSpec.Os = nil
		marshaledCurrentSpec, err := json.Marshal(renderedCurrentSpec)
		require.NoError(err)

		mockReadWriter.EXPECT().ReadFile(currentPath).Return(marshaledCurrentSpec, nil)
		bootcHost := createTestBootcHost(bootedImage)
		mockBootcClient.EXPECT().Status(ctx).Return(bootcHost, nil)

		rollbackSpec, err := createTestSpec(bootedImage)
		require.NoError(err)
		mockReadWriter.EXPECT().WriteFile(rollbackPath, rollbackSpec, gomock.Any()).Return(nil)

		err = s.PrepareRollback(ctx)
		require.NoError(err)
	})

	t.Run("writes the os image from bootc when the current spec os image is empty", func(t *testing.T) {
		bootedImage := "flightctl-device:v1"

		renderedCurrentSpec := createRenderedTestSpec("")
		renderedCurrentSpec.Os.Image = ""
		marshaledCurrentSpec, err := json.Marshal(renderedCurrentSpec)
		require.NoError(err)

		mockReadWriter.EXPECT().ReadFile(currentPath).Return(marshaledCurrentSpec, nil)
		bootcHost := createTestBootcHost(bootedImage)
		mockBootcClient.EXPECT().Status(ctx).Return(bootcHost, nil)

		rollbackSpec, err := createTestSpec(bootedImage)
		require.NoError(err)
		mockReadWriter.EXPECT().WriteFile(rollbackPath, rollbackSpec, gomock.Any()).Return(nil)

		err = s.PrepareRollback(ctx)
		require.NoError(err)
	})

	t.Run("error reading bootc status", func(t *testing.T) {
		renderedCurrentSpec := createRenderedTestSpec("")
		renderedCurrentSpec.Os.Image = ""
		marshaledCurrentSpec, err := json.Marshal(renderedCurrentSpec)
		require.NoError(err)

		mockReadWriter.EXPECT().ReadFile(currentPath).Return(marshaledCurrentSpec, nil)
		mockBootcClient.EXPECT().Status(ctx).Return(nil, ErrGettingBootcStatus)

		err = s.PrepareRollback(ctx)
		require.ErrorIs(err, ErrGettingBootcStatus)
	})
}

func TestRollback(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	currentPath := "test/current.json"
	desiredPath := "test/desired.json"
	s := &SpecManager{
		deviceReadWriter: mockReadWriter,
		currentPath:      currentPath,
		desiredPath:      desiredPath,
	}

	t.Run("error when copy fails", func(t *testing.T) {
		copyErr := errors.New("failure to copy file")
		mockReadWriter.EXPECT().CopyFile(currentPath, desiredPath).Return(copyErr)

		err := s.Rollback()
		require.ErrorIs(err, ErrCopySpec)
	})

	t.Run("copies the current spec to the desired spec", func(t *testing.T) {
		mockReadWriter.EXPECT().CopyFile(currentPath, desiredPath).Return(nil)
		err := s.Rollback()
		require.NoError(err)
	})
}

func TestSetClient(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := client.NewMockManagement(ctrl)

	t.Run("sets the client", func(t *testing.T) {
		s := &SpecManager{}
		s.SetClient(mockClient)
		require.Equal(mockClient, s.managementClient)
	})
}

func TestIsUpdating(t *testing.T) {
	require := require.New(t)

	t.Run("versions are defined and not equal", func(t *testing.T) {
		res := IsUpdating(
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: "4"},
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: "9"},
		)
		require.True(res)
	})

	t.Run("versions are defined and equal", func(t *testing.T) {
		res := IsUpdating(
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: "4"},
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: "4"},
		)
		require.False(res)
	})

	t.Run("versions are not set", func(t *testing.T) {
		res := IsUpdating(
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: ""},
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: ""},
		)
		require.False(res)
	})
}

func TestGetDesired(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := client.NewMockManagement(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	desiredPath := "test/desired.json"
	rollbackPath := "test/rollback.json"
	deviceName := "test-device"
	backoff := wait.Backoff{
		Steps: 1,
	}
	s := &SpecManager{
		backoff:          backoff,
		log:              log.NewPrefixLogger("test"),
		deviceName:       deviceName,
		deviceReadWriter: mockReadWriter,
		desiredPath:      desiredPath,
		rollbackPath:     rollbackPath,
		managementClient: mockClient,
	}

	image := "flightctl-device:v2"
	specErr := errors.New("problem with spec")

	t.Run("error reading desired spec", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(nil, specErr)

		_, err := s.GetDesired(ctx, "1")
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("error reading rollback spec", func(t *testing.T) {
		desiredSpec, err := createTestSpec(image)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)
		mockReadWriter.EXPECT().ReadFile(rollbackPath).Return(nil, specErr)

		_, err = s.GetDesired(ctx, "1")
		require.ErrorIs(err, ErrReadingRenderedSpec)
	})

	t.Run("error when get management api call fails", func(t *testing.T) {
		renderedVersion := "1"

		renderedDesiredSpec := createRenderedTestSpec(image)
		marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)

		rollbackSpec, err := createEmptyTestSpec()
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(rollbackPath).Return(rollbackSpec, nil)

		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(nil, http.StatusServiceUnavailable, specErr)

		_, err = s.GetDesired(ctx, renderedVersion)
		require.ErrorIs(err, ErrGettingDeviceSpec)
	})

	t.Run("desired spec is returned when management api returns no content", func(t *testing.T) {
		renderedVersion := "1"

		renderedDesiredSpec := createRenderedTestSpec(image)
		marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)

		rollbackSpec, err := createEmptyTestSpec()
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(rollbackPath).Return(rollbackSpec, nil)

		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(nil, http.StatusNoContent, nil)

		specResult, err := s.GetDesired(ctx, renderedVersion)
		require.NoError(err)
		require.Equal(renderedDesiredSpec, specResult)
	})

	t.Run("spec from the api response has the same RenderedVersion as desired", func(t *testing.T) {
		renderedVersion := "1"

		renderedDesiredSpec := createRenderedTestSpec(image)
		marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)

		rollbackSpec, err := createEmptyTestSpec()
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(rollbackPath).Return(rollbackSpec, nil)

		apiResponse := &v1alpha1.RenderedDeviceSpec{RenderedVersion: renderedVersion}
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(apiResponse, 200, nil)

		specResult, err := s.GetDesired(ctx, renderedVersion)
		require.NoError(err)
		require.Equal(renderedDesiredSpec, specResult)
	})

	t.Run("spec from the api response has a different RenderedVersion as desired", func(t *testing.T) {
		renderedDesiredSpec := createRenderedTestSpec(image)
		marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)

		rollbackSpec, err := createEmptyTestSpec()
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(rollbackPath).Return(rollbackSpec, nil)

		// Api is returning a rendered version that is different from the read desired spec
		apiResponse := &v1alpha1.RenderedDeviceSpec{RenderedVersion: "5"}
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(apiResponse, 200, nil)

		// The difference results in a write call for the desired spec
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

		specResult, err := s.GetDesired(ctx, "1")
		require.NoError(err)
		require.Equal(apiResponse, specResult)
	})

	t.Run("error when writing the desired spec fails", func(t *testing.T) {
		renderedDesiredSpec := createRenderedTestSpec(image)
		marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)

		rollbackSpec, err := createEmptyTestSpec()
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(rollbackPath).Return(rollbackSpec, nil)

		// Api is returning a renderd version that is different from the read desired spec
		apiResponse := &v1alpha1.RenderedDeviceSpec{RenderedVersion: "5"}
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(apiResponse, 200, nil)

		// The difference results in a write call for the desired spec
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(specErr)

		_, err = s.GetDesired(ctx, "1")
		require.ErrorIs(err, ErrWritingRenderedSpec)
	})
}

func Test_getRenderedFromManagementAPIWithRetry(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deviceName := "test-device"
	mockClient := client.NewMockManagement(ctrl)
	s := &SpecManager{
		deviceName:       deviceName,
		managementClient: mockClient,
	}

	t.Run("request error", func(t *testing.T) {
		requestErr := errors.New("failed to make request for spec")
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, gomock.Any()).Return(nil, http.StatusInternalServerError, requestErr)

		_, err := s.getRenderedFromManagementAPIWithRetry(ctx, "1", &v1alpha1.RenderedDeviceSpec{})
		require.ErrorIs(err, ErrGettingDeviceSpec)
	})

	t.Run("response status code has no content", func(t *testing.T) {
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, gomock.Any()).Return(nil, http.StatusNoContent, nil)

		_, err := s.getRenderedFromManagementAPIWithRetry(ctx, "1", &v1alpha1.RenderedDeviceSpec{})
		require.ErrorIs(err, ErrNoContent)
	})

	t.Run("response status code has conflict", func(t *testing.T) {
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, gomock.Any()).Return(nil, http.StatusConflict, nil)

		_, err := s.getRenderedFromManagementAPIWithRetry(ctx, "1", &v1alpha1.RenderedDeviceSpec{})
		require.ErrorIs(err, ErrNoContent)
	})

	t.Run("response is nil", func(t *testing.T) {
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, gomock.Any()).Return(nil, http.StatusOK, nil)

		_, err := s.getRenderedFromManagementAPIWithRetry(ctx, "1", &v1alpha1.RenderedDeviceSpec{})
		require.ErrorIs(err, ErrNilResponse)
	})

	t.Run("makes a request with empty params if no rendered version is passed", func(tt *testing.T) {
		respSpec := createRenderedTestSpec("requested-image:latest")
		params := &v1alpha1.GetRenderedDeviceSpecParams{}
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, params).Return(respSpec, http.StatusOK, nil)

		rendered := &v1alpha1.RenderedDeviceSpec{}
		success, err := s.getRenderedFromManagementAPIWithRetry(ctx, "", rendered)
		require.NoError(err)
		require.True(success)
		require.Equal(respSpec, rendered)
	})

	t.Run("makes a request with the passed renderedVersion when set", func(tt *testing.T) {
		respSpec := createRenderedTestSpec("requested-image:latest")
		renderedVersion := "24"
		params := &v1alpha1.GetRenderedDeviceSpecParams{KnownRenderedVersion: &renderedVersion}
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, params).Return(respSpec, http.StatusOK, nil)

		rendered := &v1alpha1.RenderedDeviceSpec{}
		success, err := s.getRenderedFromManagementAPIWithRetry(ctx, "24", rendered)
		require.NoError(err)
		require.True(success)
		require.Equal(respSpec, rendered)
	})
}

func Test_pathFromType(t *testing.T) {
	require := require.New(t)

	s := &SpecManager{
		currentPath:  "test/current.json",
		desiredPath:  "test/desired.json",
		rollbackPath: "test/rollback.json",
	}

	testCases := []struct {
		name          string
		specType      Type
		expectedPath  string
		expectedError error
	}{
		{
			name:         "current path resolves",
			specType:     Current,
			expectedPath: s.currentPath,
		},
		{
			name:         "desired path resolves",
			specType:     Desired,
			expectedPath: s.desiredPath,
		},
		{
			name:         "rollback path resolves",
			specType:     Rollback,
			expectedPath: s.rollbackPath,
		},
		{
			name:          "invalid spec type",
			specType:      "rainbow",
			expectedError: ErrInvalidSpecType,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			path, err := s.pathFromType(testCase.specType)

			if testCase.expectedError != nil {
				require.ErrorIs(err, testCase.expectedError)
				return
			}

			require.NoError(err)
			require.Equal(testCase.expectedPath, path)
		})
	}
}

func Test_getNextRenderedVersion(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name                string
		renderedVersion     string
		nextRenderedVersion string
		expectedError       error
	}{
		{
			name:                "empty rendered version returns an empty string",
			renderedVersion:     "",
			nextRenderedVersion: "",
		},
		{
			name:                "increments the rendered version",
			renderedVersion:     "1",
			nextRenderedVersion: "2",
		},
		{
			name:            "errors when the rendered version cannot be parsed",
			renderedVersion: "not-a-number",
			expectedError:   ErrParseRenderedVersion,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			nextVersion, err := getNextRenderedVersion(testCase.renderedVersion)

			if testCase.expectedError != nil {
				require.ErrorIs(err, testCase.expectedError)
				return
			}

			require.NoError(err)
			require.Equal(testCase.nextRenderedVersion, nextVersion)
		})
	}
}

func Test_getRenderedVersion(t *testing.T) {
	require := require.New(t)

	s := &SpecManager{
		log: log.NewPrefixLogger("test"),
	}

	testCases := []struct {
		name                    string
		currentRenderedVersion  string
		desiredRenderedVersion  string
		rollbackRenderedVersion string
		expectedReturnValue     string
		expectedError           error
	}{
		{
			name:                   "no current rendered version returns an empty string",
			currentRenderedVersion: "",
			expectedReturnValue:    "",
		},
		{
			name:                    "all versions are equal",
			currentRenderedVersion:  "1",
			desiredRenderedVersion:  "1",
			rollbackRenderedVersion: "1",
			expectedReturnValue:     "2",
		},
		{
			name:                    "current not equal to rollback",
			currentRenderedVersion:  "1",
			desiredRenderedVersion:  "1",
			rollbackRenderedVersion: "3",
			expectedReturnValue:     "1",
		},
		{
			name:                    "desired not equal to rollback",
			currentRenderedVersion:  "1",
			desiredRenderedVersion:  "3",
			rollbackRenderedVersion: "1",
			expectedReturnValue:     "1",
		},
		{
			name:                    "current not equal to desired or rollback",
			currentRenderedVersion:  "3",
			desiredRenderedVersion:  "1",
			rollbackRenderedVersion: "1",
			expectedReturnValue:     "3",
		},
		{
			name:                    "all versions are different",
			currentRenderedVersion:  "1",
			desiredRenderedVersion:  "2",
			rollbackRenderedVersion: "3",
			expectedReturnValue:     "1",
		},
		{
			name:                    "invalid versions",
			currentRenderedVersion:  "one",
			desiredRenderedVersion:  "one",
			rollbackRenderedVersion: "one",
			expectedError:           ErrParseRenderedVersion,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			renderedVersion, err := s.getRenderedVersion(
				testCase.currentRenderedVersion,
				testCase.desiredRenderedVersion,
				testCase.rollbackRenderedVersion,
			)

			if testCase.expectedError != nil {
				require.ErrorIs(err, testCase.expectedError)
				return
			}

			require.NoError(err)
			require.Equal(testCase.expectedReturnValue, renderedVersion)
		})
	}
}

func Test_isOsSame(t *testing.T) {
	require := require.New(t)

	digest := "sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5"
	digestTwo := "sha256:bd1b50c5a1df1bcb701e3556075a890c4e4a87765f985ee3a4b87df91db98c4d"
	testCases := []struct {
		name           string
		osOne          *v1alpha1.DeviceOSSpec
		osTwo          *v1alpha1.DeviceOSSpec
		expectedResult bool
	}{
		{
			name:           "both are nil",
			osOne:          nil,
			osTwo:          nil,
			expectedResult: true,
		},
		{
			name:  "one is defined and the other nil",
			osOne: nil,
			osTwo: &v1alpha1.DeviceOSSpec{
				Image: "device:v1",
			},
			expectedResult: false,
		},
		{
			name: "images are the same",
			osOne: &v1alpha1.DeviceOSSpec{
				Image: "device:v1",
			},
			osTwo: &v1alpha1.DeviceOSSpec{
				Image: "device:v1",
			},
			expectedResult: true,
		},
		{
			name: "images and digests are the same",
			osOne: &v1alpha1.DeviceOSSpec{
				Image:       "device:v1",
				ImageDigest: &digest,
			},
			osTwo: &v1alpha1.DeviceOSSpec{
				Image:       "device:v1",
				ImageDigest: &digest,
			},
			expectedResult: true,
		}, {
			name: "images are the same but digests are different",
			osOne: &v1alpha1.DeviceOSSpec{
				Image:       "device:v1",
				ImageDigest: &digest,
			},
			osTwo: &v1alpha1.DeviceOSSpec{
				Image:       "device:v1",
				ImageDigest: &digestTwo,
			},
			expectedResult: false,
		},
		{
			name: "images are different and digests are nil",
			osOne: &v1alpha1.DeviceOSSpec{
				Image: "device:v1",
			},
			osTwo: &v1alpha1.DeviceOSSpec{
				Image: "device:v2",
			},
			expectedResult: false,
		},
		{
			name: "images are different and digests are different",
			osOne: &v1alpha1.DeviceOSSpec{
				Image:       "device:v1",
				ImageDigest: &digest,
			},
			osTwo: &v1alpha1.DeviceOSSpec{
				Image:       "device:v2",
				ImageDigest: &digestTwo,
			},
			expectedResult: false,
		},
		{
			name: "images are different but digests are the same",
			osOne: &v1alpha1.DeviceOSSpec{
				Image:       "device:v1",
				ImageDigest: &digest,
			},
			osTwo: &v1alpha1.DeviceOSSpec{
				Image:       "device:v2",
				ImageDigest: &digest,
			},
			expectedResult: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := isOsSame(testCase.osOne, testCase.osTwo)
			require.Equal(testCase.expectedResult, result)
		})
	}
}

func Test_areImagesEquivalent(t *testing.T) {
	require := require.New(t)

	digest := "sha256:6cf77c2a98dd4df274d14834fab9424b6e96ef3ed3f49f792b27c163763f52b5"
	digestTwo := "sha256:bd1b50c5a1df1bcb701e3556075a890c4e4a87765f985ee3a4b87df91db98c4d"
	testCases := []struct {
		name           string
		imageOne       *Image
		imageTwo       *Image
		expectedResult bool
	}{
		{
			name:           "both are nil",
			imageOne:       nil,
			imageTwo:       nil,
			expectedResult: true,
		},
		{
			name:           "one is defined and the other nil",
			imageOne:       nil,
			imageTwo:       &Image{},
			expectedResult: false,
		},
		{
			name: "image digests are equal",
			imageOne: &Image{
				Digest: digest,
			},
			imageTwo: &Image{
				Digest: digest,
			},
			expectedResult: true,
		},
		{
			name: "image digests are not equal",
			imageOne: &Image{
				Digest: digest,
			},
			imageTwo: &Image{
				Digest: digestTwo,
			},
			expectedResult: false,
		},
		{
			name: "image bases match",
			imageOne: &Image{
				Base: "flightct-device",
			},
			imageTwo: &Image{
				Base: "flightct-device",
			},
			expectedResult: true,
		},
		{
			name: "image bases match when one image has a digest defined",
			imageOne: &Image{
				Base:   "flightct-device",
				Digest: digest,
			},
			imageTwo: &Image{
				Base: "flightct-device",
			},
			expectedResult: true,
		},
		{
			name: "image bases are different",
			imageOne: &Image{
				Base: "flightct-device",
			},
			imageTwo: &Image{
				Base: "device-os",
			},
			expectedResult: false,
		},
		{
			name: "image bases are different but digests are identical",
			imageOne: &Image{
				Base:   "flightct-device",
				Digest: digest,
			},
			imageTwo: &Image{
				Base:   "device-os",
				Digest: digest,
			},
			expectedResult: true,
		},
		{
			name: "image bases match and one has a tag",
			imageOne: &Image{
				Base: "flightct-device",
				Tag:  "v1",
			},
			imageTwo: &Image{
				Base: "flightct-device",
			},
			expectedResult: false,
		},
		{
			name: "image bases match and tags match",
			imageOne: &Image{
				Base: "flightct-device",
				Tag:  "v2",
			},
			imageTwo: &Image{
				Base: "flightct-device",
				Tag:  "v2",
			},
			expectedResult: true,
		},
		{
			name: "image bases match and tags are different",
			imageOne: &Image{
				Base: "flightct-device",
				Tag:  "v2",
			},
			imageTwo: &Image{
				Base: "flightct-device",
				Tag:  "v9",
			},
			expectedResult: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := areImagesEquivalent(testCase.imageOne, testCase.imageTwo)
			require.Equal(testCase.expectedResult, result)
		})
	}
}

func createTestSpec(image string) ([]byte, error) {
	spec := createRenderedTestSpec(image)
	return json.Marshal(spec)
}

func createRenderedTestSpec(image string) *v1alpha1.RenderedDeviceSpec {
	spec := v1alpha1.RenderedDeviceSpec{
		RenderedVersion: "1",
		Os: &v1alpha1.DeviceOSSpec{
			Image: image,
		},
	}
	return &spec
}

func createEmptyTestSpec() ([]byte, error) {
	return json.Marshal(&v1alpha1.RenderedDeviceSpec{})
}

func createTestBootcHost(image string) *container.BootcHost {
	host := &container.BootcHost{}
	host.Status.Booted.Image.Image.Image = image
	return host
}
