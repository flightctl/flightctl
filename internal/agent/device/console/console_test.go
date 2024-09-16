package console

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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

		consoleController *ConsoleController

		desired     *api.RenderedDeviceSpec
		testCommand *exec.Cmd
		sessionId   string
		deviceName  string
	)

	BeforeEach(func() {
		ctrl := gomock.NewController(GinkgoT())
		DeferCleanup(ctrl.Finish)

		logger = log.NewPrefixLogger("TestConsoleController")

		sessionId = "session-1"
		deviceName = "test-device"

		ctx = context.TODO()
		mockGrpcClient = NewMockRouterServiceClient(ctrl)
		mockStreamClient = NewMockRouterService_StreamClient(ctrl)
		mockExecutor = executer.NewMockExecuter(ctrl)

		consoleController = NewConsoleController(mockGrpcClient, deviceName, mockExecutor, logger)

		desired = &api.RenderedDeviceSpec{
			Console: &api.DeviceConsole{
				SessionID: sessionId,
			},
		}

		testCommand = exec.Command("bash", "-i", "-l")
	})

	When("Test Console.Sync()", func() {
		It("no desired console", func() {
			consoleController.active = true

			err := consoleController.Sync(ctx, &api.RenderedDeviceSpec{})
			Expect(err).ToNot(HaveOccurred())
			Expect(consoleController.active).To(BeFalse())
		})

		It("active console with new desired console", func() {
			consoleController.active = true
			consoleController.streamClient = mockStreamClient

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
			Expect(consoleController.active).To(BeTrue())
		})

		It("no desired console with active stream failure", func() {
			consoleController.active = true
			consoleController.streamClient = mockStreamClient

			mockStreamClient.EXPECT().CloseSend().Return(errors.New("close send error"))

			err := consoleController.Sync(ctx, &api.RenderedDeviceSpec{})
			Expect(err).To(HaveOccurred())
			Expect(consoleController.active).To(BeTrue())
		})

		It("active console with same session ID", func() {
			consoleController.currentStreamID = sessionId

			mockStreamClient.EXPECT().Recv().Return(nil, nil).AnyTimes()
			mockStreamClient.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
			mockStreamClient.EXPECT().CloseSend().Return(nil).AnyTimes()
			mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(mockStreamClient, nil)
			mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(testCommand)

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
		})

		It("console session was closed", func() {
			consoleController.lastClosedStream = sessionId

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
		})

		It("no gRPC client available", func() {
			consoleController.grpcClient = nil

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
		})

		It("error creating shell process", func() {
			consoleController.lastClosedStream = ""

			mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(nil, errors.New("shell creation error"))
			testCommand.Process = nil
			mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(testCommand)

			err := consoleController.Sync(ctx, desired)
			Expect(err).To(HaveOccurred())
		})

		It("error creating console stream client", func() {
			mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(nil, errors.New("stream creation error"))
			mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(testCommand)

			err := consoleController.Sync(ctx, desired)
			Expect(err).To(HaveOccurred())
		})

		It("successful console sync", func() {
			mockStreamClient.EXPECT().Recv().Return(nil, nil).AnyTimes()
			mockStreamClient.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
			mockStreamClient.EXPECT().CloseSend().Return(nil).AnyTimes()
			mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(mockStreamClient, nil)
			mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(testCommand)

			err := consoleController.Sync(ctx, desired)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
