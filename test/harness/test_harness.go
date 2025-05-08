package harness

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	workerserver "github.com/flightctl/flightctl/internal/worker_server"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

type TestHarness struct {
	// internals for agent
	cancelAgentCtx context.CancelFunc
	agentFinished  chan struct{}
	agentConfig    *agent_config.Config

	// internals for server
	serverListener net.Listener

	// error handler for go routines
	goRoutineErrorHandler func(error)

	// internals for client context
	cancelCtx context.CancelFunc

	// K8S client mock
	mockK8sClient *k8sclient.MockK8SClient
	ctrl          *gomock.Controller

	// attributes for the test harness
	Context     context.Context
	Server      *apiserver.Server
	Agent       *agent.Agent
	Client      *apiclient.ClientWithResponses
	Store       *store.Store
	TestDirPath string
}

// NewTestHarness creates a new test harness and returns a new test harness
// The test harness can be used from testing code to interact with a
// set of agent/server/store instances.
// It provides the necessary elements to perform tests against the agent and server.
func NewTestHarness(testDirPath string, goRoutineErrorHandler func(error)) (*TestHarness, error) {

	err := makeTestDirs(testDirPath, []string{"/etc/flightctl/certs", "/etc/issue.d/", "/var/lib/flightctl/"})
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness failed creating temporary directories: %w", err)
	}

	serverCfg := *config.NewDefault()
	serverLog := log.InitLogs()
	serverLog.SetLevel(logrus.DebugLevel)
	serverLog.SetOutput(os.Stdout)

	// create store
	store, dbName, err := testutil.NewTestStore(serverCfg, serverLog)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}
	serverCfg.Database.Name = dbName

	// create certs
	serverCfg.Service.CertStore = filepath.Join(testDirPath, "etc", "flightctl", "certs")
	ca, serverCerts, _, err := testutil.NewTestCerts(&serverCfg)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	provider := testutil.NewTestProvider(serverLog)
	// create server

	apiServer, listener, err := testutil.NewTestApiServer(serverLog, &serverCfg, store, ca, serverCerts, provider)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	ctrl := gomock.NewController(GinkgoT())
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	workerServer := workerserver.New(&serverCfg, serverLog, store, provider, mockK8sClient)

	agentServer, agentListener, err := testutil.NewTestAgentServer(serverLog, &serverCfg, store, ca, serverCerts, provider)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	serverCfg.Service.Address = listener.Addr().String()
	serverCfg.Service.AgentEndpointAddress = agentListener.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())

	// start main api server
	go func() {
		os.Setenv(auth.DisableAuthEnvKey, "true")
		err := apiServer.Run(ctx)
		if err != nil {
			// provide a wrapper to allow require.NoError or ginkgo handling
			goRoutineErrorHandler(fmt.Errorf("error starting main api server: %w", err))
		}
		cancel()
	}()

	go func() {
		err := workerServer.Run(context.WithValue(ctx, consts.InternalRequestCtxKey, true))
		if err != nil {
			// provide a wrapper to allow require.NoError or ginkgo handling
			goRoutineErrorHandler(fmt.Errorf("error starting worker server: %w", err))
		}
		cancel()
	}()

	// start agent api server
	go func() {
		err := agentServer.Run(ctx)
		if err != nil {
			// provide a wrapper to allow require.NoError or ginkgo handling
			goRoutineErrorHandler(fmt.Errorf("error starting main agent api server: %w", err))
		}
		cancel()
	}()

	fetchSpecInterval := util.Duration(2 * time.Second)
	statusUpdateInterval := util.Duration(2 * time.Second)

	os.Setenv(agent_config.TestRootDirEnvKey, testDirPath)
	cfg := agent_config.NewDefault()
	// TODO: remove the cert/key modifications from default, and start storing
	// the test harness files for those in the testDir/etc/flightctl/certs path
	cfg.EnrollmentService = config.EnrollmentService{
		Config:               *client.NewDefault(),
		EnrollmentUIEndpoint: "https://flightctl.ui/",
	}
	cfg.EnrollmentService.Service = client.Service{
		Server:               "https://" + serverCfg.Service.AgentEndpointAddress,
		CertificateAuthority: "/etc/flightctl/certs/ca.crt",
	}
	cfg.EnrollmentService.AuthInfo = client.AuthInfo{
		ClientCertificate: "/etc/flightctl/certs/client-enrollment.crt",
		ClientKey:         "/etc/flightctl/certs/client-enrollment.key",
	}
	cfg.SpecFetchInterval = fetchSpecInterval
	cfg.StatusUpdateInterval = statusUpdateInterval
	if err := cfg.Complete(); err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	// create client to talk to the server
	client, err := testutil.NewClient("https://"+listener.Addr().String(), ca.GetCABundleX509())
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	testHarness := &TestHarness{
		agentConfig:           cfg,
		serverListener:        listener,
		goRoutineErrorHandler: goRoutineErrorHandler,
		Context:               ctx,
		cancelCtx:             cancel,
		Server:                apiServer,
		Client:                client,
		Store:                 &store,
		mockK8sClient:         mockK8sClient,
		ctrl:                  ctrl,
		TestDirPath:           testDirPath}

	testHarness.StartAgent()

	return testHarness, nil
}

func (h *TestHarness) Cleanup() {
	h.StopAgent()
	// stop any pending API requests
	h.cancelCtx()
	// unset env var for the test dir path
	os.Unsetenv(agent_config.TestRootDirEnvKey)
	h.ctrl.Finish()
}

func (h *TestHarness) AgentDownloadedCertificate() bool {
	_, err := os.Stat(filepath.Join(h.TestDirPath, "/var/lib/flightctl/certs/agent.crt"))
	if err != nil && os.IsNotExist(err) {
		return false
	}
	return true
}

func (h *TestHarness) StopAgent() {
	h.cancelAgentCtx()
	<-h.agentFinished
}

func (h *TestHarness) StartAgent() {
	agentLog := log.NewPrefixLogger("")
	agentInstance := agent.New(agentLog, h.agentConfig, "")

	ctx, cancel := context.WithCancel(context.Background())
	h.agentFinished = make(chan struct{})
	// start agent
	go func() {
		err := agentInstance.Run(ctx)
		close(h.agentFinished)
		if err != nil && !errors.Is(err, context.Canceled) {
			h.goRoutineErrorHandler(fmt.Errorf("error starting agent: %w", err))
		}
	}()

	h.cancelAgentCtx = cancel
}

func (h *TestHarness) GetMockK8sClient() *k8sclient.MockK8SClient {
	return h.mockK8sClient
}

func (h *TestHarness) RestartAgent() {
	h.StopAgent()
	h.StartAgent()
}

func makeTestDirs(tmpDirPath string, paths []string) error {
	for _, path := range paths {
		err := os.MkdirAll(filepath.Join(tmpDirPath, path), 0755)
		if err != nil {
			return err
		}
	}
	return nil
}
