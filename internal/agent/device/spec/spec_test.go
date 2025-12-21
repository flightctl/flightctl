package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/spec/audit"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestBootstrapCheckRollback(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockOSClient := os.NewMockClient(ctrl)
	log := log.NewPrefixLogger("test")

	s := &manager{
		log:              log,
		deviceReadWriter: mockReadWriter,
		osClient:         mockOSClient,
		cache:            newCache(log),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("no rollback: bootstrap case empty desired spec", func(t *testing.T) {
		wantIsRollback := false
		device, err := createTestDeviceBytes("")
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(device, nil)

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
		desiredSpec, err := createTestDeviceBytes(desiredImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(desiredSpec, nil)

		// rollbackSpec
		rollbackSpec, err := createTestDeviceBytes(rollbackImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(rollbackSpec, nil)

		// bootc OSStatus
		bootcStatus := container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = bootedImage
		osStatus := os.Status{BootcHost: bootcStatus}
		mockOSClient.EXPECT().Status(ctx).Return(&osStatus, nil)

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
		desiredSpec, err := createTestDeviceBytes(desiredImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(desiredSpec, nil)

		// rollbackSpec
		rollbackSpec, err := createTestDeviceBytes(rollbackImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(rollbackSpec, nil)

		// bootc OSStatus
		bootcStatus := container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = bootedImage
		osStatus := os.Status{BootcHost: bootcStatus}
		mockOSClient.EXPECT().Status(ctx).Return(&osStatus, nil)

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
	mockPolicyManager := policy.NewMockManager(ctrl)
	log := log.NewPrefixLogger("test")

	cache := newCache(log)
	queue := newQueueManager(
		defaultSpecQueueMaxSize,
		defaultSpecRequeueMaxRetries,
		defaultSpecPollConfig,
		mockPolicyManager,
		cache,
		log,
	)

	s := &manager{
		deviceReadWriter: mockReadWriter,
		cache:            newCache(log),
		queue:            queue,
	}

	writeErr := errors.New("write failure")

	t.Run("error writing current file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)
		err := s.Initialize(context.Background())
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("error writing desired file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// desired
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)

		err := s.Initialize(context.Background())
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("error writing rollback file", func(t *testing.T) {
		// current
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// desired
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		// rollback
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(writeErr)

		err := s.Initialize(context.Background())
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("successful initialization", func(t *testing.T) {
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Times(3).Return(nil)
		mockPolicyManager.EXPECT().IsReady(context.Background(), gomock.Any()).Return(true).Times(2)
		err := s.Initialize(context.Background())
		require.NoError(err)
	})
}

func TestEnsure(t *testing.T) {
	require := require.New(t)

	fileErr := errors.New("invalid file")

	t.Run("error checking if file exists", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockReadWriter := fileio.NewMockReadWriter(ctrl)
		mockPriorityQueue := NewMockPriorityQueue(ctrl)
		log := log.NewPrefixLogger("test")

		s := &manager{
			log:              log,
			deviceReadWriter: mockReadWriter,
			queue:            mockPriorityQueue,
			cache:            newCache(log),
		}

		mockReadWriter.EXPECT().PathExists(gomock.Any()).Return(false, fileErr)
		err := s.Ensure()
		require.ErrorIs(err, errors.ErrCheckingFileExists)
	})

	t.Run("error writing file when it does not exist", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockReadWriter := fileio.NewMockReadWriter(ctrl)
		mockPriorityQueue := NewMockPriorityQueue(ctrl)
		log := log.NewPrefixLogger("test")

		s := &manager{
			log:              log,
			deviceReadWriter: mockReadWriter,
			queue:            mockPriorityQueue,
			cache:            newCache(log),
		}

		// First loop: check all 3 files for allMissing detection (all return false)
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(3).Return(false, nil)
		// Second loop: check current file, find it missing, attempt write and fail
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(1).Return(false, nil)
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(fileErr)
		err := s.Ensure()
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("files are written when they don't exist", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockReadWriter := fileio.NewMockReadWriter(ctrl)
		mockPriorityQueue := NewMockPriorityQueue(ctrl)
		log := log.NewPrefixLogger("test")

		s := &manager{
			log:              log,
			deviceReadWriter: mockReadWriter,
			queue:            mockPriorityQueue,
			cache:            newCache(log),
		}

		// First loop: check first file, it exists, break early
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(1).Return(true, nil)
		// Second loop: check all 3 files
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(1).Return(true, nil)  // current exists
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(1).Return(true, nil)  // desired exists
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(1).Return(false, nil) // rollback missing
		// Write the missing file
		mockReadWriter.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Times(1).Return(nil)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return([]byte(`{}`), nil).Times(3)
		mockPriorityQueue.EXPECT().Add(gomock.Any(), gomock.Any()).Times(1)
		err := s.Ensure()
		require.NoError(err)
	})

	t.Run("no files are written when they all exist", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockReadWriter := fileio.NewMockReadWriter(ctrl)
		mockPriorityQueue := NewMockPriorityQueue(ctrl)
		log := log.NewPrefixLogger("test")

		s := &manager{
			log:              log,
			deviceReadWriter: mockReadWriter,
			queue:            mockPriorityQueue,
			cache:            newCache(log),
		}

		// First loop: check all 3 files for allMissing detection - all exist, break early
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(1).Return(true, nil)
		// Second loop: check each file before potentially creating - all exist
		mockReadWriter.EXPECT().PathExists(gomock.Any()).Times(3).Return(true, nil)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return([]byte(`{}`), nil).Times(3)
		mockPriorityQueue.EXPECT().Add(gomock.Any(), gomock.Any()).Times(1)
		err := s.Ensure()
		require.NoError(err)
	})
}

func TestRead(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	log := log.NewPrefixLogger("test")

	s := &manager{
		log:              log,
		deviceReadWriter: mockReadWriter,
		cache:            newCache(log),
	}

	t.Run("ensure proper error handling on read failure", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(nil, errors.New("read gone wrong"))
		_, err := s.Read(Current)
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
	})

	t.Run("reads a device spec", func(t *testing.T) {
		image := "flightctl-device:v1"
		spec, err := createTestDeviceBytes(image)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(gomock.Any()).Return(spec, nil)

		fromRead, err := s.Read(Current)
		require.NoError(err)
		require.Equal(image, fromRead.Spec.Os.Image)
	})
}

func Test_readRenderedSpecFromFile(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReader := fileio.NewMockReader(ctrl)
	filePath := "test/path/spec.json"

	t.Run("error when the file does not exist", func(t *testing.T) {
		mockReader.EXPECT().ReadFile(filePath).Return(nil, errors.ErrNotExist)

		_, err := readDeviceFromFile(mockReader, filePath)
		require.ErrorIs(err, errors.ErrMissingRenderedSpec)
	})

	t.Run("error reading file when it does exist", func(t *testing.T) {
		mockReader.EXPECT().ReadFile(filePath).Return(nil, errors.New("cannot read"))

		_, err := readDeviceFromFile(mockReader, filePath)
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
	})

	t.Run("error when the file is not a valid spec", func(t *testing.T) {
		invalidSpec := []byte("Not json data for a spec")
		mockReader.EXPECT().ReadFile(filePath).Return(invalidSpec, nil)

		_, err := readDeviceFromFile(mockReader, filePath)
		require.ErrorIs(err, errors.ErrUnmarshalSpec)
	})

	t.Run("returns the read spec", func(t *testing.T) {
		image := "flightctl-device:v1"
		spec, err := createTestDeviceBytes(image)
		require.NoError(err)
		mockReader.EXPECT().ReadFile(gomock.Any()).Return(spec, nil)

		specFromRead, err := readDeviceFromFile(mockReader, filePath)
		require.NoError(err)
		require.Equal(image, specFromRead.Spec.Os.Image)
	})
}

func Test_writeRenderedToFile(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWriter := fileio.NewMockWriter(ctrl)
	filePath := "path/to/write"
	spec := createTestRenderedDevice("test-image")

	marshaled, err := json.Marshal(spec)
	require.NoError(err)

	t.Run("returns an error when the write fails", func(t *testing.T) {
		writeErr := errors.New("some failure")
		mockWriter.EXPECT().WriteFile(filePath, marshaled, fileio.DefaultFilePermissions).Return(writeErr)

		err = writeDeviceToFile(mockWriter, spec, filePath)
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("writes a rendered spec", func(t *testing.T) {
		mockWriter.EXPECT().WriteFile(filePath, marshaled, fileio.DefaultFilePermissions).Return(nil)

		err = writeDeviceToFile(mockWriter, spec, filePath)
		require.NoError(err)
	})
}

func TestUpgrade(t *testing.T) {
	require := require.New(t)
	desiredPath := "test/desired.json"
	currentPath := "test/current.json"
	rollbackPath := "test/rollback.json"
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
				desiredSpec, err := createTestDeviceBytes("flightctl-device:v2")
				require.NoError(err)
				mrw.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)
				mrw.EXPECT().WriteFile(currentPath, desiredSpec, gomock.Any()).Return(specErr)
			},
			expectedError: errors.ErrWritingRenderedSpec,
		},
		{
			name: "error writing the rollback spec",
			setupMocks: func(mrw *fileio.MockReadWriter, mpq *MockPriorityQueue) {
				desiredSpec, err := createTestDeviceBytes("flightctl-device:v2")
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
				desiredSpec, err := createTestDeviceBytes("flightctl-device:v2")
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
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockPriorityQueue := NewMockPriorityQueue(ctrl)
			log := log.NewPrefixLogger("test")

			s := &manager{
				log:              log,
				deviceReadWriter: mockReadWriter,
				desiredPath:      desiredPath,
				currentPath:      currentPath,
				rollbackPath:     rollbackPath,
				queue:            mockPriorityQueue,
				cache:            newCache(log),
			}

			s.cache.current.renderedVersion = "1"
			s.cache.desired.renderedVersion = "2"

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
	log := log.NewPrefixLogger("test")

	desiredPath := "test/desired.json"
	currentPath := "test/current.json"
	s := &manager{
		log:              log,
		deviceReadWriter: mockReadWriter,
		desiredPath:      desiredPath,
		currentPath:      currentPath,
		cache:            newCache(log),
	}

	t.Run("both specs are empty", func(t *testing.T) {
		s.cache.current.osVersion = ""
		s.cache.desired.osVersion = ""

		osUpdate := s.IsOSUpdate()
		require.Equal(false, osUpdate)
	})

	t.Run("current and desired os images are the same", func(t *testing.T) {
		image := "flightctl-device:v2"

		s.cache.current.osVersion = image
		s.cache.desired.osVersion = image

		osUpdate := s.IsOSUpdate()
		require.Equal(false, osUpdate)
	})

	t.Run("current and desired os images are different", func(t *testing.T) {
		currentImage := "flightctl-device:v2"
		desiredImage := "flightctl-deivce:v3"
		s.cache.current.osVersion = currentImage
		s.cache.desired.osVersion = desiredImage

		osUpdate := s.IsOSUpdate()
		require.Equal(true, osUpdate)
	})
}

func TestCheckOsReconciliation(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockOSClient := os.NewMockClient(ctrl)
	log := log.NewPrefixLogger("test")

	desiredPath := "test/desired.json"
	s := &manager{
		log:              log,
		deviceReadWriter: mockReadWriter,
		osClient:         mockOSClient,
		desiredPath:      desiredPath,
		cache:            newCache(log),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emptySpec, err := json.Marshal(newVersionedDevice("0"))
	require.NoError(err)

	t.Run("error getting bootc status", func(t *testing.T) {
		bootcErr := errors.New("bootc problem")
		mockOSClient.EXPECT().Status(ctx).Return(nil, bootcErr)

		_, _, err := s.CheckOsReconciliation(ctx)
		require.ErrorIs(err, errors.ErrGettingBootcStatus)
	})

	t.Run("error reading desired spec", func(t *testing.T) {
		bootcStatus := container.BootcHost{}
		osStatus := os.Status{BootcHost: bootcStatus}
		mockOSClient.EXPECT().Status(ctx).Return(&osStatus, nil)

		readErr := errors.New("unable to read file")
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(emptySpec, readErr)

		_, _, err = s.CheckOsReconciliation(ctx)
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
	})

	t.Run("desired os is not set in the spec", func(t *testing.T) {
		bootedImage := "flightctl-device:v1"

		bootcStatus := container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = bootedImage
		osStatus := os.Status{BootcHost: bootcStatus}
		mockOSClient.EXPECT().Status(ctx).Return(&osStatus, nil)

		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(emptySpec, nil)

		bootedOSImage, desiredImageIsBooted, err := s.CheckOsReconciliation(ctx)
		require.NoError(err)
		require.Equal(bootedOSImage, bootedImage)
		require.Equal(false, desiredImageIsBooted)
	})

	t.Run("booted image and desired image are different", func(t *testing.T) {
		bootedImage := "flightctl-device:v1"
		desiredImage := "flightctl-device:v2"

		bootcStatus := container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = bootedImage
		osStatus := os.Status{BootcHost: bootcStatus}
		mockOSClient.EXPECT().Status(ctx).Return(&osStatus, nil)

		desiredSpec, err := createTestDeviceBytes(desiredImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)

		bootedOSImage, desiredImageIsBooted, err := s.CheckOsReconciliation(ctx)
		require.NoError(err)
		require.Equal(bootedOSImage, bootedImage)
		require.Equal(false, desiredImageIsBooted)
	})

	t.Run("booted image and desired image are the same", func(t *testing.T) {
		image := "flightctl-device:v2"

		bootcStatus := container.BootcHost{}
		bootcStatus.Status.Booted.Image.Image.Image = image
		osStatus := os.Status{BootcHost: bootcStatus}
		mockOSClient.EXPECT().Status(ctx).Return(&osStatus, nil)

		desiredSpec, err := createTestDeviceBytes(image)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(desiredPath).Return(desiredSpec, nil)

		bootedOSImage, desiredImageIsBooted, err := s.CheckOsReconciliation(ctx)
		require.NoError(err)
		require.Equal(bootedOSImage, image)
		require.Equal(true, desiredImageIsBooted)
	})
}

func TestCreateRollback(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockReadWriter := fileio.NewMockReadWriter(ctrl)
	mockOSClient := os.NewMockClient(ctrl)
	log := log.NewPrefixLogger("test")

	currentPath := "test/current.json"
	rollbackPath := "test/rollback.json"
	s := &manager{
		log:              log,
		deviceReadWriter: mockReadWriter,
		osClient:         mockOSClient,
		currentPath:      currentPath,
		rollbackPath:     rollbackPath,
		cache:            newCache(log),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emptySpec, err := createEmptyTestSpec()
	require.NoError(err)

	specErr := errors.New("unable to use spec")

	t.Run("error reading current spec", func(t *testing.T) {
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(emptySpec, specErr)

		err = s.CreateRollback(ctx)
		require.ErrorIs(err, errors.ErrReadingRenderedSpec)
	})

	t.Run("error writing rollback spec", func(t *testing.T) {
		currentImage := "flightctl-device:v1"

		currentSpec, err := createTestDeviceBytes(currentImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(currentSpec, nil)

		mockReadWriter.EXPECT().WriteFile(rollbackPath, gomock.Any(), gomock.Any()).Return(specErr)

		err = s.CreateRollback(ctx)
		require.ErrorIs(err, errors.ErrWritingRenderedSpec)
	})

	t.Run("writes the os image from the current spec when it is defined", func(t *testing.T) {
		currentImage := "flightctl-device:v1"

		currentSpec, err := createTestDeviceBytes(currentImage)
		require.NoError(err)
		mockReadWriter.EXPECT().ReadFile(currentPath).Return(currentSpec, nil)

		rollbackSpec, err := createTestDeviceBytes(currentImage)
		require.NoError(err)
		mockReadWriter.EXPECT().WriteFile(rollbackPath, rollbackSpec, gomock.Any()).Return(nil)

		err = s.CreateRollback(ctx)
		require.NoError(err)
	})

	t.Run("writes the os image from bootc when the current spec os is nil", func(t *testing.T) {
		bootedImage := "flightctl-device:v1"

		renderedCurrent := createTestRenderedDevice("")
		renderedCurrent.Spec.Os = nil
		marshaledCurrentSpec, err := json.Marshal(renderedCurrent)
		require.NoError(err)

		mockReadWriter.EXPECT().ReadFile(currentPath).Return(marshaledCurrentSpec, nil)
		bootcHost := createTestBootcHost(bootedImage)
		osStatus := os.Status{BootcHost: *bootcHost}
		mockOSClient.EXPECT().Status(ctx).Return(&osStatus, nil)

		rollbackSpec, err := createTestDeviceBytes(bootedImage)
		require.NoError(err)
		mockReadWriter.EXPECT().WriteFile(rollbackPath, rollbackSpec, gomock.Any()).Return(nil)

		err = s.CreateRollback(ctx)
		require.NoError(err)
	})

	t.Run("writes the os image from bootc when the current spec os image is empty", func(t *testing.T) {
		bootedImage := "flightctl-device:v1"

		renderedCurrent := createTestRenderedDevice("")
		renderedCurrent.Spec.Os.Image = ""
		marshaledCurrentSpec, err := json.Marshal(renderedCurrent)
		require.NoError(err)

		mockReadWriter.EXPECT().ReadFile(currentPath).Return(marshaledCurrentSpec, nil)
		bootcHost := createTestBootcHost(bootedImage)
		osStatus := os.Status{BootcHost: *bootcHost}
		mockOSClient.EXPECT().Status(ctx).Return(&osStatus, nil)

		rollbackSpec, err := createTestDeviceBytes(bootedImage)
		require.NoError(err)
		mockReadWriter.EXPECT().WriteFile(rollbackPath, rollbackSpec, gomock.Any()).Return(nil)

		err = s.CreateRollback(ctx)
		require.NoError(err)
	})

	t.Run("error reading bootc status", func(t *testing.T) {
		renderedCurrent := createTestRenderedDevice("")
		renderedCurrent.Spec.Os.Image = ""
		marshaledCurrentSpec, err := json.Marshal(renderedCurrent)
		require.NoError(err)

		mockReadWriter.EXPECT().ReadFile(currentPath).Return(marshaledCurrentSpec, nil)
		mockOSClient.EXPECT().Status(ctx).Return(nil, errors.ErrGettingBootcStatus)

		err = s.CreateRollback(ctx)
		require.ErrorIs(err, errors.ErrGettingBootcStatus)
	})
}

func TestRollback(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name               string
		setupMocks         func(mockPolicyManager *policy.MockManager)
		wantSetFailed      bool
		currentVersion     string
		desiredVersion     string
		wantCurrentVersion string
		wantDesiredVersion string
		wantNextVersion    string
		expectedError      error
	}{
		{
			name:               "rollback to previous",
			currentVersion:     "1",
			desiredVersion:     "2",
			wantCurrentVersion: "1",
			wantDesiredVersion: "1",
			wantNextVersion:    "1",
			wantSetFailed:      false,
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true)
			},
		},
		{
			name:               "rollback to previous set failed",
			currentVersion:     "1",
			desiredVersion:     "2",
			wantCurrentVersion: "1",
			wantDesiredVersion: "1",
			wantNextVersion:    "1",
			wantSetFailed:      true,
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true)
			},
		},
		{
			name:               "rollback to previous desired failed",
			currentVersion:     "1",
			desiredVersion:     "2",
			wantCurrentVersion: "1",
			wantDesiredVersion: "1",
			wantNextVersion:    "1",
			wantSetFailed:      true,
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true)
			},
		},
		{
			name:               "rollback to previous multiple versions",
			currentVersion:     "1",
			desiredVersion:     "9",
			wantCurrentVersion: "1",
			wantDesiredVersion: "1",
			wantNextVersion:    "1",
			wantSetFailed:      false,
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true)
			},
		},
		{
			name:               "rollback to steady state is idempotent",
			currentVersion:     "1",
			desiredVersion:     "1",
			wantCurrentVersion: "1",
			wantDesiredVersion: "1",
			wantNextVersion:    "1",
			wantSetFailed:      false,
			setupMocks: func(mockPolicyManager *policy.MockManager) {
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Download).Return(true)
				mockPolicyManager.EXPECT().IsReady(gomock.Any(), policy.Update).Return(true)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tmpDir := t.TempDir()
			readWriter := fileio.NewReadWriter()
			readWriter.SetRootdir(tmpDir)
			log := log.NewPrefixLogger("test")
			mockPolicyManager := policy.NewMockManager(ctrl)
			pub := newPublisher("testDevice", poll.NewConfig(10*time.Millisecond, 1.5), "0", nil, log)
			cache := newCache(log)
			queue := newQueueManager(
				defaultSpecQueueMaxSize,
				defaultSpecRequeueMaxRetries,
				defaultSpecPollConfig,
				mockPolicyManager,
				cache,
				log,
			)
			dataDir := tmpDir

			s := &manager{
				log:              log,
				deviceReadWriter: readWriter,
				queue:            queue,
				currentPath:      filepath.Join(dataDir, string(Current)+".json"),
				desiredPath:      filepath.Join(dataDir, string(Desired)+".json"),
				rollbackPath:     filepath.Join(dataDir, string(Rollback)+".json"),
				publisher:        pub,
				watcher:          pub.Watch(),
				cache:            newCache(log),
			}

			tc.setupMocks(mockPolicyManager)

			err := s.write(ctx, Current, newVersionedDevice(tc.currentVersion), audit.ReasonInitialization)
			require.NoError(err)
			err = s.write(ctx, Desired, newVersionedDevice(tc.desiredVersion), audit.ReasonInitialization)
			require.NoError(err)

			opts := []RollbackOption{}
			if tc.wantSetFailed {
				opts = append(opts, WithSetFailed())
			}

			// ensure memory
			assertRenderedVersions(t, s, tc.currentVersion, tc.desiredVersion)
			// ensure disk state
			assertDiskVersions(t, s, tc.currentVersion, tc.desiredVersion)

			err = s.Rollback(ctx, opts...)
			require.NoError(err)

			// ensure memory
			assertRenderedVersions(t, s, tc.wantCurrentVersion, tc.wantDesiredVersion)
			// ensure disk state
			assertDiskVersions(t, s, tc.wantCurrentVersion, tc.wantDesiredVersion)

			// ensure the current spec was returned to the priority queue and available
			next, requeue, err := s.GetDesired(ctx)
			require.NoError(err)
			require.False(requeue)
			require.Equal(tc.wantNextVersion, next.Version())

			// ensure desired vesion is tracked as failed
			if tc.wantSetFailed {
				version, err := stringToInt64(tc.desiredVersion)
				require.NoError(err)
				require.True(s.queue.IsFailed(version))
			}
		})
	}
}

func assertRenderedVersions(t *testing.T, s *manager, wantCurrent, wantDesired string) {
	require.Equal(t, wantCurrent, s.cache.getRenderedVersion(Current))
	require.Equal(t, wantDesired, s.cache.getRenderedVersion(Desired))
}

func assertDiskVersions(t *testing.T, s *manager, wantCurrent, wantDesired string) {
	current, err := s.Read(Current)
	require.NoError(t, err)
	require.Equal(t, wantCurrent, current.Version())

	desired, err := s.Read(Desired)
	require.NoError(t, err)
	require.Equal(t, wantDesired, desired.Version())
}

func TestIsUpgrading(t *testing.T) {
	require := require.New(t)

	t.Run("versions are defined and not equal", func(t *testing.T) {
		res := IsUpgrading(
			newVersionedDevice("4"),
			newVersionedDevice("9"),
		)
		require.True(res)
	})

	t.Run("versions are defined and equal", func(t *testing.T) {
		res := IsUpgrading(
			newVersionedDevice("4"),
			newVersionedDevice("4"),
		)
		require.False(res)
	})

	t.Run("versions are not set", func(t *testing.T) {
		res := IsUpgrading(
			newVersionedDevice(""),
			newVersionedDevice(""),
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
	image := "flightctl-device:v2"
	specErr := errors.New("problem with spec")

	// Define the test cases
	testCases := []struct {
		name           string
		setupMocks     func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement, mpm *policy.MockManager)
		expectedDevice *v1beta1.Device
		expectedError  error
	}{
		{
			name: "error reading desired spec",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement, mpm *policy.MockManager) {
				mrw.EXPECT().ReadFile(desiredPath).Return(nil, specErr)
			},
			expectedDevice: nil,
			expectedError:  errors.ErrReadingRenderedSpec,
		},
		{
			name: "desired spec is returned when management api returns no content",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement, mpm *policy.MockManager) {
				renderedDesiredSpec := createTestRenderedDevice(image)
				marshaledDesiredSpec, err := json.Marshal(renderedDesiredSpec)
				require.NoError(err)

				mrw.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)
				mpq.EXPECT().Add(gomock.Any(), gomock.Any())
				mpq.EXPECT().Next(gomock.Any()).Return(renderedDesiredSpec, true)

			},
			expectedDevice: createTestRenderedDevice(image),
			expectedError:  nil,
		},
		{
			name: "spec from the api response has the same Version as desired",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement, mpm *policy.MockManager) {
				renderedDesiredSpec := newVersionedDevice("2") // same as cache desired version "2"
				renderedDesiredSpec.Spec = &v1beta1.DeviceSpec{
					Os: &v1beta1.DeviceOsSpec{
						Image: image,
					},
				}

				// Since consumeLatest returns false, it reads from disk first
				marshaledDesiredSpec, _ := json.Marshal(renderedDesiredSpec)
				mrw.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)
				mpq.EXPECT().Add(gomock.Any(), gomock.Any())
				mpq.EXPECT().Next(gomock.Any()).Return(renderedDesiredSpec, true)
				// No WriteFile expectation since version is the same, so no write should occur
				// No Sync call since we're not consuming from subscription
			},
			expectedDevice: func() *v1beta1.Device {
				device := newVersionedDevice("2")
				device.Spec = &v1beta1.DeviceSpec{
					Os: &v1beta1.DeviceOsSpec{
						Image: image,
					},
				}
				return device
			}(),
			expectedError: nil,
		},
		{
			name: "error when writing the desired spec fails",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement, mpm *policy.MockManager) {
				// Create a device with version "3" (newer than cache desired version "2")
				device := newVersionedDevice("3")
				device.Spec = &v1beta1.DeviceSpec{
					Os: &v1beta1.DeviceOsSpec{
						Image: image,
					},
				}

				// Since consumeLatest returns false, it reads from disk first
				oldDesiredSpec := newVersionedDevice("2")
				marshaledOldDesiredSpec, _ := json.Marshal(oldDesiredSpec)
				mrw.EXPECT().ReadFile(desiredPath).Return(marshaledOldDesiredSpec, nil)

				mpq.EXPECT().Add(gomock.Any(), gomock.Any())
				mpq.EXPECT().Next(gomock.Any()).Return(device, true)
				// No Sync call since we're not consuming from subscription

				// API is returning a rendered version that is different from the read desired spec
				// Test doesn't need to push to publisher

				// The difference results in a write call for the desired spec
				mrw.EXPECT().WriteFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(specErr)
			},
			expectedDevice: nil,
			expectedError:  errors.ErrWritingRenderedSpec,
		},
		{
			name: "rejects older version than current desired",
			setupMocks: func(mpq *MockPriorityQueue, mrw *fileio.MockReadWriter, mc *client.MockManagement, mpm *policy.MockManager) {
				// Mock the Read(Desired) call that happens when no new spec is consumed
				desiredSpec := newVersionedDevice("2")
				marshaledDesiredSpec, err := json.Marshal(desiredSpec)
				require.NoError(err)
				mrw.EXPECT().ReadFile(desiredPath).Return(marshaledDesiredSpec, nil)

				// Create a device with version "1" (older than cache desired version "2")
				olderDevice := newVersionedDevice("1")
				olderDevice.Spec = &v1beta1.DeviceSpec{
					Os: &v1beta1.DeviceOsSpec{
						Image: image,
					},
				}
				mpq.EXPECT().Add(gomock.Any(), gomock.Any())
				mpq.EXPECT().Next(gomock.Any()).Return(olderDevice, true)
			},
			expectedDevice: nil,
			expectedError:  fmt.Errorf("version 1 is older than current desired version 2"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl.Finish()
			ctrl := gomock.NewController(t)
			mockClient := client.NewMockManagement(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)
			mockPriorityQueue := NewMockPriorityQueue(ctrl)
			mockPolicyManager := policy.NewMockManager(ctrl)
			mockWatcher := NewMockWatcher(ctrl)

			log := log.NewPrefixLogger("test")

			s := &manager{
				log:              log,
				deviceReadWriter: mockReadWriter,
				desiredPath:      desiredPath,
				rollbackPath:     rollbackPath,
				queue:            mockPriorityQueue,
				cache:            newCache(log),
				watcher:          mockWatcher,
				policyManager:    mockPolicyManager,
			}

			s.cache.current.renderedVersion = "1"
			s.cache.desired.renderedVersion = "2"

			mockWatcher.EXPECT().TryPop().Return(nil, false, nil).AnyTimes()

			tc.setupMocks(
				mockPriorityQueue,
				mockReadWriter,
				mockClient,
				mockPolicyManager,
			)

			specResult, _, err := s.GetDesired(ctx)
			if tc.expectedError != nil {
				require.Error(err)
				require.Contains(err.Error(), tc.expectedError.Error())
				require.Nil(specResult)
				return
			}
			require.NoError(err)
			require.NotNil(specResult)
			require.Equal(tc.expectedDevice, specResult)
		})
	}
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

func Test_getVersion(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name            string
		currentVersion  string
		desiredVersion  string
		desiredIsFailed bool
		expectedVersion string
	}{
		{
			name:            "no current rendered version returns an empty string",
			currentVersion:  "",
			expectedVersion: "",
		},
		{
			name:            "desired is failed",
			currentVersion:  "1",
			desiredVersion:  "2",
			desiredIsFailed: true,
			expectedVersion: "2",
		},
		{
			name:            "reconciled",
			currentVersion:  "1",
			desiredVersion:  "1",
			expectedVersion: "1",
		},
		{
			name:            "current and desired skew",
			currentVersion:  "1",
			desiredVersion:  "3",
			desiredIsFailed: true,
			expectedVersion: "3",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockPriorityQueue := NewMockPriorityQueue(ctrl)
			log := log.NewPrefixLogger("test")

			s := &manager{
				log:   log,
				queue: mockPriorityQueue,
				cache: newCache(log),
			}

			s.cache.current.renderedVersion = tt.currentVersion
			s.cache.desired.renderedVersion = tt.desiredVersion

			var isFailed bool
			if tt.desiredIsFailed {
				isFailed = true
			}
			mockPriorityQueue.EXPECT().IsFailed(gomock.Any()).Return(isFailed)
			renderedVersion, err := s.getRenderedVersion()
			require.NoError(err)
			require.Equal(tt.expectedVersion, renderedVersion)
		})
	}
}

func createTestDeviceBytes(image string) ([]byte, error) {
	spec := createTestRenderedDevice(image)
	return json.Marshal(spec)
}

func createTestRenderedDevice(image string) *v1beta1.Device {
	device := newVersionedDevice("1")
	spec := v1beta1.DeviceSpec{
		Os: &v1beta1.DeviceOsSpec{
			Image: image,
		},
	}
	device.Spec = &spec
	return device
}

func createEmptyTestSpec() ([]byte, error) {
	return json.Marshal(newVersionedDevice(""))
}

func createTestBootcHost(image string) *container.BootcHost {
	host := &container.BootcHost{}
	host.Status.Booted.Image.Image.Image = image
	return host
}
