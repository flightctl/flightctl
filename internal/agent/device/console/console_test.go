package console

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
)

func TestConsoleController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Console Suite")
}

var _ = Describe("ConsoleController", func() {
	var (
		logger           *log.PrefixLogger
		ctx              context.Context
		mockGrpcClient   *MockRouterServiceClient
		mockStreamClient *MockRouterService_StreamClient
		mockExecutor     *executer.MockExecuter
		desired          *v1alpha1.RenderedDeviceSpec
		bashCommand      *exec.Cmd
		sessionId        string
		deviceName       string
	)

	BeforeEach(func() {
		ctrl := gomock.NewController(GinkgoT())
		defer ctrl.Finish()

		logger = log.NewPrefixLogger("test")

		ctx = context.TODO()
		mockGrpcClient = NewMockRouterServiceClient(ctrl)
		mockStreamClient = NewMockRouterService_StreamClient(ctrl)
		mockExecutor = executer.NewMockExecuter(ctrl)

		desired = &v1alpha1.RenderedDeviceSpec{
			Console: &v1alpha1.DeviceConsole{
				SessionID: sessionId,
			},
		}

		bashCommand = exec.Command("bash", "-i", "-l")
		sessionId = "session-1"
		deviceName = "test-device"
	})

	When("Test Console.Sync()", func() {
		It("no desired console", func() {
			mockStreamClient.EXPECT().CloseSend().Return(nil)

			consoleController := NewConsoleController(mockGrpcClient, deviceName, mockExecutor, logger)
			consoleController.active = true
			consoleController.streamClient = mockStreamClient

			err := consoleController.Sync(ctx, &v1alpha1.RenderedDeviceSpec{})
			Expect(err).ToNot(HaveOccurred())
			Expect(consoleController.active).To(BeFalse())
		})

		It("active console with same session ID", func() {
			consoleController := NewConsoleController(mockGrpcClient, deviceName, mockExecutor, logger)
			consoleController.active = true
			consoleController.streamClient = mockStreamClient
			consoleController.currentStreamID = sessionId

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
		})

		It("console session was closed", func() {
			consoleController := NewConsoleController(mockGrpcClient, deviceName, mockExecutor, logger)
			consoleController.lastClosedStream = sessionId

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
		})

		It("no gRPC client available", func() {
			consoleController := NewConsoleController(nil, deviceName, mockExecutor, logger)

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
		})

		It("error creating shell process", func() {
			consoleController := NewConsoleController(mockGrpcClient, deviceName, mockExecutor, logger)
			consoleController.grpcClient = mockGrpcClient
			consoleController.streamClient = nil
			consoleController.lastClosedStream = ""

			mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(nil, errors.New("shell error"))
			bashCommand.Process = nil
			mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(bashCommand)

			err := consoleController.Sync(ctx, desired)
			Expect(err).To(HaveOccurred())
		})

		It("error creating console stream client", func() {
			consoleController := NewConsoleController(mockGrpcClient, deviceName, mockExecutor, logger)
			consoleController.grpcClient = mockGrpcClient

			mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(nil, errors.New("stream error"))
			mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(bashCommand)

			err := consoleController.Sync(ctx, desired)
			Expect(err).To(HaveOccurred())
		})

		It("successful console sync", func() {
			consoleController := NewConsoleController(mockGrpcClient, deviceName, mockExecutor, logger)
			consoleController.active = false
			consoleController.grpcClient = mockGrpcClient

			mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(mockStreamClient, nil)
			mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(exec.Command("test"))

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
