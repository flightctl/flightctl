package console

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type ConsoleControllerSuite struct {
	suite.Suite
	ctrl              *gomock.Controller
	consoleController *ConsoleController
	ctx               context.Context
	mockGrpcClient    *MockRouterServiceClient
	mockStreamClient  *MockRouterService_StreamClient
	mockExecutor      *executer.MockExecuter
	testCommand       *exec.Cmd
	desired           *api.RenderedDeviceSpec
}

func (suite *ConsoleControllerSuite) SetupTest() {
	suite.ctrl = gomock.NewController(suite.T())
	logger := log.NewPrefixLogger("TestConsoleController")

	sessionId := "session-1"
	deviceName := "test-device"

	suite.ctx = context.TODO()
	suite.mockGrpcClient = NewMockRouterServiceClient(suite.ctrl)
	suite.mockStreamClient = NewMockRouterService_StreamClient(suite.ctrl)
	suite.mockExecutor = executer.NewMockExecuter(suite.ctrl)

	suite.consoleController = NewController(suite.mockGrpcClient, deviceName, suite.mockExecutor, logger)

	suite.desired = &api.RenderedDeviceSpec{
		Console: &api.DeviceConsole{
			SessionID: sessionId,
		},
	}

	suite.testCommand = exec.Command("bash", "-i", "-l")
}

func (suite *ConsoleControllerSuite) TearDownTest() {
	suite.ctrl.Finish()
}

func (suite *ConsoleControllerSuite) TestNoDesiredConsole() {
	suite.consoleController.active = true

	err := suite.consoleController.Sync(suite.ctx, &api.RenderedDeviceSpec{})
	suite.NoError(err)
	suite.False(suite.consoleController.active)
}

func (suite *ConsoleControllerSuite) TestActiveConsoleWithNewDesiredConsole() {
	suite.consoleController.active = true
	suite.consoleController.streamClient = suite.mockStreamClient

	err := suite.consoleController.Sync(suite.ctx, suite.desired)
	suite.NoError(err)
	suite.True(suite.consoleController.active)
}

func (suite *ConsoleControllerSuite) TestNoDesiredConsoleWithActiveStreamFailure() {
	suite.consoleController.active = true
	suite.consoleController.streamClient = suite.mockStreamClient

	suite.mockStreamClient.EXPECT().CloseSend().Return(errors.New("close send error"))

	err := suite.consoleController.Sync(suite.ctx, &api.RenderedDeviceSpec{})
	suite.Error(err)
	suite.True(suite.consoleController.active)
}

func (suite *ConsoleControllerSuite) TestActiveConsoleWithSameSessionID() {
	suite.consoleController.currentStreamID = suite.desired.Console.SessionID

	suite.mockStreamClient.EXPECT().Recv().Return(nil, nil).AnyTimes()
	suite.mockStreamClient.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
	suite.mockStreamClient.EXPECT().CloseSend().Return(nil).AnyTimes()
	suite.mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(suite.mockStreamClient, nil)
	suite.mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(suite.testCommand)

	err := suite.consoleController.Sync(suite.ctx, suite.desired)
	suite.NoError(err)
}

func (suite *ConsoleControllerSuite) TestConsoleSessionWasClosed() {
	suite.consoleController.lastClosedStream = suite.desired.Console.SessionID

	err := suite.consoleController.Sync(suite.ctx, suite.desired)
	suite.NoError(err)
}

func (suite *ConsoleControllerSuite) TestNoGrpcClientAvailable() {
	suite.consoleController.grpcClient = nil

	err := suite.consoleController.Sync(suite.ctx, suite.desired)
	suite.NoError(err)
}

func (suite *ConsoleControllerSuite) TestErrorCreatingShellProcess() {
	suite.consoleController.lastClosedStream = ""

	suite.mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(nil, errors.New("shell creation error"))
	suite.testCommand.Process = nil
	suite.mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(suite.testCommand)

	err := suite.consoleController.Sync(suite.ctx, suite.desired)
	suite.Error(err)
}

func (suite *ConsoleControllerSuite) TestErrorCreatingConsoleStreamClient() {
	suite.mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(nil, errors.New("stream creation error"))
	suite.mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(suite.testCommand)

	err := suite.consoleController.Sync(suite.ctx, suite.desired)
	suite.Error(err)
}

func (suite *ConsoleControllerSuite) TestSuccessfulConsoleSync() {
	suite.mockStreamClient.EXPECT().Recv().Return(nil, nil).AnyTimes()
	suite.mockStreamClient.EXPECT().Send(gomock.Any()).Return(nil).AnyTimes()
	suite.mockStreamClient.EXPECT().CloseSend().Return(nil).AnyTimes()
	suite.mockGrpcClient.EXPECT().Stream(gomock.Any()).Return(suite.mockStreamClient, nil)
	suite.mockExecutor.EXPECT().CommandContext(gomock.Any(), gomock.Any(), gomock.Any()).Return(suite.testCommand)

	err := suite.consoleController.Sync(suite.ctx, suite.desired)
	suite.NoError(err)
}

func TestConsoleControllerSuite(t *testing.T) {
	suite.Run(t, new(ConsoleControllerSuite))
}
