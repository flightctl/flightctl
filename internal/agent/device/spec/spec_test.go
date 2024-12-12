package spec

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
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
	mockBootcClient := client.NewMockBootc(ctrl)

	s := &manager{
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

func TestInitialize(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	s := &manager{
		deviceReadWriter: mockReadWriter,
	}

	writeErr := errors.New("write failure")

	t.Run("error writing current file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)
		err := s.Initialize()

		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("error writing desired file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// desired
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)

		err := s.Initialize()
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("error writing rollback file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// desired
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// rollback
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)

		err := s.Initialize()
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
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
	mockPriorityQueue := NewMockPriorityQueue(ctrl)

	s := &manager{
		log:              log.NewPrefixLogger("test"),
		deviceReadWriter: mockReadWriter,
		queue:            mockPriorityQueue,
	}

	fileErr := errors.New("invalid file")

	t.Run("error checking if file exists", func(t *testing.T) {
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(false, fileErr)
		err := s.Ensure()
		require.ErrorIs(err, errors.ErrCheckingFileExists)
	})

	t.Run("error writing file when it does not exist", func(t *testing.T) {
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(false, nil)
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(fileErr)
		err := s.Ensure()
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("files are written when they don't exist", func(t *testing.T) {
		// First two files checked exist
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(2).Return(true, nil)
		// Third file does not exist, so then expect a write
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(1).Return(false, nil)
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Times(1).Return(nil)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return([]byte(`{}`), nil).Times(3)
		mockPriorityQueue.EXPECT().Add(gomock.Any()).Return(nil).Times(1)
		err := s.Ensure()
		require.NoError(err)
	})

	t.Run("no files are written when they all exist", func(t *testing.T) {
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(3).Return(true, nil)
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return([]byte(`{}`), nil).Times(3)
		mockPriorityQueue.EXPECT().Add(gomock.Any()).Return(nil).Times(1)
		err := s.Ensure()
		require.NoError(err)
	})
}

func TestRead(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	s := &manager{
		log:              log.NewPrefixLogger("test"),
		deviceReadWriter: mockReadWriter,
	}

	t.Run("ensure proper error handling on read failure", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(nil, errors.New("read gone wrong"))
		_, err := s.Read(Current)
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
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
		require.ErrorIs(err, errors.ErrMissingRenderedSpec)
	})

	t.Run("error reading file when it does exist", func(t *testing.T) {
		mockReader.EXPECT().ReadFile(filePath).Return(nil, errors.New("cannot read"))

		_, err := readRenderedSpecFromFile(mockReader, filePath)
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
	})

	t.Run("error when the file is not a valid spec", func(t *testing.T) {
		invalidSpec := []byte("Not json data for a spec")
		mockReader.EXPECT().ReadFile(filePath).Return(invalidSpec, nil)

		_, err := readRenderedSpecFromFile(mockReader, filePath)
		require.ErrorIs(err, errors.ErrUnmarshalSpec)
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
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("writes a rendered spec", func(t *testing.T) {
		mockWriter.EXPECT().WriteFile(filePath, marshaled, fileio.DefaultFilePermissions).Return(nil)

		err = writeRenderedToFile(mockWriter, spec, filePath)
		require.NoError(err)
	})
}

func TestUpgrade(t *testing.T) {
	require := require.New(t)
	desiredPath := "test/desired.json"
	currentPath := "test/current.json"
	rollbackPath := "test/rollback/json"
	specErr := errors.New("error with spec")
	emptySpec, err := createEmptyTestSpec()
	require.NoError(err)

	testCases := []struct {
		name          string
		setupMocks    func(mockReadWriter *fileio.MockReadWriter, mockPriorityQueue *MockPriorityQueue)
		expectedError error
	}{
		{
			name: "error reading desired spec",
			setupMocks: func(mrw *fileio.MockReadWriter, mpq *MockPriorityQueue) {
				mrw.EXPECT().ReadFile(desiredPath).Return(nil, specErr)
			},
			expectedError: errors.ErrReadingRenderedSpec,
		},
		{
			name: "error writing desired spec to current",
			setupMocks: func(mrw *fileio.MockReadWriter, mpq *MockPriorityQueue) {
				desiredSpec, err := createTestSpec("flightctl-device:v2")
				require.NoError(err)
				mrw.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)
				mrw.EXPECT().WriteFile(currentPath, desiredSpec, gomock.Any()).Return(specErr)
			},
			expectedError: errors.ErrWritingRenderedSpec,
		},
		{
			name: "error writing the rollback spec",
			setupMocks: func(mrw *fileio.MockReadWriter, mpq *MockPriorityQueue) {
				desiredSpec, err := createTestSpec("flightctl-device:v2")
				require.NoError(err)
				mrw.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)
				mrw.EXPECT().WriteFile(currentPath, desiredSpec, gomock.Any()).Return(nil)
				mrw.EXPECT().WriteFile(rollbackPath, emptySpec, gomock.Any()).Return(specErr)
			},
			expectedError: errors.ErrWritingRenderedSpec,
		},
		{
			name: "clears out the rollback spec",
			setupMocks: func(mrw *fileio.MockReadWriter, mpq *MockPriorityQueue) {
				desiredSpec, err := createTestSpec("flightctl-device:v2")
				require.NoError(err)
				mrw.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)
				mrw.EXPECT().WriteFile(currentPath, desiredSpec, gomock.Any()).Return(nil)
				mrw.EXPECT().WriteFile(rollbackPath, emptySpec, gomock.Any()).Return(nil)
				mpq.EXPECT().Remove(gomock.Any())
			},
			expectedError: nil,
		},
	}

	for _, tc := range testCases {
		tc := tc // Capture range variable
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockPriorityQueue := NewMockPriorityQueue(ctrl)

			s := &manager{
				log:              log.NewPrefixLogger("test"),
				deviceReadWriter: mockReadWriter,
				desiredPath:      desiredPath,
				currentPath:      currentPath,
				rollbackPath:     rollbackPath,
				queue:            mockPriorityQueue,
			}

			s.currentRenderedVersion = "1"
			s.desiredRenderedVersion = "2"

			tc.setupMocks(mockReadWriter, mockPriorityQueue)

			err := s.Upgrade(context.Background())

			if tc.expectedError != nil {
				require.ErrorIs(err, tc.expectedError)
				return
			}
			require.NoError(err)
		})
	}
}

func TestIsOSUpdate(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	desiredPath := "test/desired.json"
	currentPath := "test/current.json"
	s := &manager{
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
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
	})

	t.Run("error reading desired spec", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(emptySpec, nil)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(nil, specErr)

		_, err := s.IsOSUpdate()
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
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
	mockBootcClient := client.NewMockBootc(ctrl)

	desiredPath := "test/desired.json"
	s := &manager{
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
		require.ErrorIs(err, errors.ErrGettingBootcStatus)
	})

	t.Run("error reading desired spec", func(t *testing.T) {
		bootcStatus := &container.BootcHost{}
		mockBootcClient.EXPECT().Status(ctx).Return(bootcStatus, nil)

		readErr := errors.New("unable to read file")
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(emptySpec, readErr)

		_, _, err = s.CheckOsReconciliation(ctx)
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
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
	mockBootcClient := client.NewMockBootc(ctrl)

	currentPath := "test/current.json"
	rollbackPath := "test/rollback.json"
	s := &manager{
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
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
	})

	t.Run("error writing rollback spec", func(t *testing.T) {
		currentImage := "flightctl-device:v1"

		currentSpec, err := createTestSpec(currentImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(currentSpec, nil)

		mockReadWriter.EXPECT().WriteFile(rollbackPath, gomock.Any(), gomock.Any()).Return(specErr)

		err = s.PrepareRollback(ctx)
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
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
		mockBootcClient.EXPECT().Status(ctx).Return(nil, errors.ErrGettingBootcStatus)

		err = s.PrepareRollback(ctx)
		require.ErrorIs(err, errors.ErrGettingBootcStatus)
	})
}

func TestRollback(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockPriorityQueue := NewMockPriorityQueue(ctrl)

	currentPath := "test/current.json"
	desiredPath := "test/desired.json"
	s := &manager{
		deviceReadWriter: mockReadWriter,
		queue:            mockPriorityQueue,
		currentPath:      currentPath,
		desiredPath:      desiredPath,
	}

	t.Run("error when copy fails", func(t *testing.T) {
		copyErr := errors.New("failure to copy file")
		mockReadWriter.EXPECT().CopyFile(currentPath, desiredPath).Return(copyErr)
		mockPriorityQueue.EXPECT().SetVersionFailed(gomock.Any())

		err := s.Rollback()
		require.ErrorIs(err, errors.ErrCopySpec)
	})

	t.Run("copies the current spec to the desired spec", func(t *testing.T) {
		currentSpec, err := createTestSpec("flightctl-device:v1")
		require.NoError(err)
		mockReadWriter.EXPECT().CopyFile(currentPath, desiredPath).Return(nil)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(currentSpec, nil)
		mockPriorityQueue.EXPECT().SetVersionFailed(gomock.Any())
		err = s.Rollback()
		require.NoError(err)
	})
}

func TestSetClient(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := client.NewMockManagement(ctrl)

	t.Run("sets the client", func(t *testing.T) {
		s := &manager{}
		s.SetClient(mockClient)
		require.Equal(mockClient, s.managementClient)
	})
}

func TestIsUpgrading(t *testing.T) {
	require := require.New(t)

	t.Run("versions are defined and not equal", func(t *testing.T) {
		res := IsUpgrading(
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: "4"},
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: "9"},
		)
		require.True(res)
	})

	t.Run("versions are defined and equal", func(t *testing.T) {
		res := IsUpgrading(
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: "4"},
			&v1alpha1.RenderedDeviceSpec{RenderedVersion: "4"},
		)
		require.False(res)
	})

	t.Run("versions are not set", func(t *testing.T) {
		res := IsUpgrading(
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	desiredPath := "test/desired.json"
	rollbackPath := "test/rollback.json"
	deviceName := "test-device"
	image := "flightctl-device:v2"
	specErr := errors.New("problem with spec")

	// Define the test cases
	testCases := []struct {
		name          string
		setupMocks    func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement)
		expectedSpec  *v1alpha1.RenderedDeviceSpec
		expectedError error
	}{
		{
			name: "error reading desired spec",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement) {
				mrw.EXPECT().ReadFile(desiredPath).Return(nil, specErr)
				mpq.EXPECT().IsVersionFailed(gomock.Any()).Return(false)
				mc.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(nil, http.StatusNoContent, nil)
			},
			expectedSpec:  nil,
			expectedError: errors.ErrReadingRenderedSpec,
		},
		{
			name: "error when get management api call fails",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement) {
				renderedDesiredSpec := createRenderedTestSpec(image)
				marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
				require.NoError(err)

				mrw.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)
				mpq.EXPECT().IsVersionFailed(gomock.Any()).Return(false)
				mc.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(nil, http.StatusServiceUnavailable, specErr)
			},
			expectedSpec:  nil,
			expectedError: errors.ErrGettingDeviceSpec,
		},
		{
			name: "desired spec is returned when management api returns no content",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement) {
				renderedDesiredSpec := createRenderedTestSpec(image)
				marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
				require.NoError(err)

				mrw.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)
				mpq.EXPECT().IsVersionFailed(gomock.Any()).Return(false)
				mpq.EXPECT().Add(gomock.Any()).Return(nil)
				mpq.EXPECT().Next().Return(&Item{Version: 2, Spec: renderedDesiredSpec}, true)

				mc.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(nil, http.StatusNoContent, nil)
			},
			expectedSpec:  createRenderedTestSpec(image),
			expectedError: nil,
		},
		{
			name: "spec from the api response has the same RenderedVersion as desired",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement) {
				renderedDesiredSpec := createRenderedTestSpec(image)
				marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
				require.NoError(err)

				mpq.EXPECT().IsVersionFailed(gomock.Any()).Return(false)
				mpq.EXPECT().Add(gomock.Any()).Return(nil)
				mpq.EXPECT().Next().Return(&Item{Version: 1, Spec: renderedDesiredSpec}, true)
				mrw.EXPECT().WriteFile(desiredPath, marshaledDesiredSpec, gomock.Any()).Return(nil)

				mc.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(renderedDesiredSpec, 200, nil)
			},
			expectedSpec:  createRenderedTestSpec(image),
			expectedError: nil,
		},
		{
			name: "error when writing the desired spec fails",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement) {
				renderedDesiredSpec := createRenderedTestSpec(image)
				mpq.EXPECT().IsVersionFailed(gomock.Any()).Return(false)
				mpq.EXPECT().Add(gomock.Any()).Return(nil)
				mpq.EXPECT().Next().Return(&Item{Version: 1, Spec: renderedDesiredSpec}, true)

				// API is returning a rendered version that is different from the read desired spec
				apiResponse := &v1alpha1.RenderedDeviceSpec{RenderedVersion: "5"}
				mc.EXPECT().GetRenderedDeviceSpec(ctx, gomock.Any(), gomock.Any()).Return(apiResponse, 200, nil)

				// The difference results in a write call for the desired spec
				mrw.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(specErr)
			},
			expectedSpec:  nil,
			expectedError: errors.ErrWritingRenderedSpec,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctrl.Finish()
			ctrl := gomock.NewController(t)
			mockClient := client.NewMockManagement(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockPriorityQueue := NewMockPriorityQueue(ctrl)

			backoff := wait.Backoff{
				Steps: 1,
			}

			s := &manager{
				backoff:          backoff,
				log:              log.NewPrefixLogger("test"),
				deviceName:       deviceName,
				deviceReadWriter: mockReadWriter,
				desiredPath:      desiredPath,
				rollbackPath:     rollbackPath,
				managementClient: mockClient,
				queue:            mockPriorityQueue,
			}

			s.currentRenderedVersion = "1"
			s.desiredRenderedVersion = "2"

			tc.setupMocks(
				mockPriorityQueue,
				mockReadWriter,
				mockClient,
			)

			specResult, _, err := s.GetDesired(ctx)
			if tc.expectedError != nil {
				require.ErrorIs(err, tc.expectedError)
				require.Nil(specResult)
				return
			}
			require.NoError(err)
			require.NotNil(specResult)
			require.Equal(tc.expectedSpec, specResult)
		})
	}
}

func Test_getRenderedFromManagementAPIWithRetry(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deviceName := "test-device"
	mockClient := client.NewMockManagement(ctrl)
	s := &manager{
		deviceName:       deviceName,
		managementClient: mockClient,
	}

	t.Run("request error", func(t *testing.T) {
		requestErr := errors.New("failed to make request for spec")
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, gomock.Any()).Return(nil, http.StatusInternalServerError, requestErr)

		_, err := s.getRenderedFromManagementAPIWithRetry(ctx, "1", &v1alpha1.RenderedDeviceSpec{})
		require.ErrorIs(err, errors.ErrGettingDeviceSpec)
	})

	t.Run("response status code has no content", func(t *testing.T) {
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, gomock.Any()).Return(nil, http.StatusNoContent, nil)

		_, err := s.getRenderedFromManagementAPIWithRetry(ctx, "1", &v1alpha1.RenderedDeviceSpec{})
		require.ErrorIs(err, errors.ErrNoContent)
	})

	t.Run("response status code has conflict", func(t *testing.T) {
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, gomock.Any()).Return(nil, http.StatusConflict, nil)

		_, err := s.getRenderedFromManagementAPIWithRetry(ctx, "1", &v1alpha1.RenderedDeviceSpec{})
		require.ErrorIs(err, errors.ErrNoContent)
	})

	t.Run("response is nil", func(t *testing.T) {
		mockClient.EXPECT().GetRenderedDeviceSpec(ctx, deviceName, gomock.Any()).Return(nil, http.StatusOK, nil)

		_, err := s.getRenderedFromManagementAPIWithRetry(ctx, "1", &v1alpha1.RenderedDeviceSpec{})
		require.ErrorIs(err, errors.ErrNilResponse)
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

	s := &manager{
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
			expectedError: errors.ErrInvalidSpecType,
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

func Test_getRenderedVersion(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name                    string
		currentRenderedVersion  string
		desiredRenderedVersion  string
		desiredIsFailed         bool
		expectedRenderedVersion string
	}{
		{
			name:                    "no current rendered version returns an empty string",
			currentRenderedVersion:  "",
			expectedRenderedVersion: "",
		},
		{
			name:                    "desired is failed",
			currentRenderedVersion:  "1",
			desiredRenderedVersion:  "2",
			desiredIsFailed:         true,
			expectedRenderedVersion: "2",
		},
		{
			name:                    "reconciled",
			currentRenderedVersion:  "1",
			desiredRenderedVersion:  "1",
			expectedRenderedVersion: "1",
		},
		{
			name:                    "current and desired skew",
			currentRenderedVersion:  "1",
			desiredRenderedVersion:  "3",
			desiredIsFailed:         true,
			expectedRenderedVersion: "3",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockPriorityQueue := NewMockPriorityQueue(ctrl)

			s := &manager{
				log:   log.NewPrefixLogger("test"),
				queue: mockPriorityQueue,
			}

			s.currentRenderedVersion = tt.currentRenderedVersion
			s.desiredRenderedVersion = tt.desiredRenderedVersion

			var isFailed bool
			if tt.desiredIsFailed {
				isFailed = true
			}
			mockPriorityQueue.EXPECT().IsVersionFailed(gomock.Any()).Return(isFailed)
			renderedVersion := s.getRenderedVersion()
			require.Equal(tt.expectedRenderedVersion, renderedVersion)
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
