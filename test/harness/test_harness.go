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
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
)

type TestHarness struct {
	// internals for agent
	cancelAgentCtx context.CancelFunc
	agentFinished  chan struct{}
	agentConfig    *agent.Config

	// internals for server
	serverListener net.Listener

	// error handler for go routines
	goRoutineErrorHandler func(error)

	// internals for client context
	cancelCtx context.CancelFunc

	// attributes for the test harness
	Context     context.Context
	Server      *server.Server
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

	// create store
	store, dbName, err := testutil.NewTestStore(serverCfg, serverLog)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}
	serverCfg.Database.Name = dbName

	// create certs
	serverCfg.Service.CertStore = filepath.Join(testDirPath, "etc", "flightctl", "certs")
	ca, serverCerts, _, clientCerts, err := testutil.NewTestCerts(&serverCfg)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	// create server
	server, listener, err := testutil.NewTestServer(serverLog, &serverCfg, store, ca, serverCerts)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	agentServer, agentListener, err := testutil.NewTestAgentServer(serverLog, &serverCfg, store, ca, serverCerts)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	serverCfg.Service.Address = listener.Addr().String()
	serverCfg.Service.AgentEndpointAddress = agentListener.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())

	// start main api server
	go func() {
		os.Setenv(auth.DisableAuthEnvKey, "true")
		err := server.Run(ctx)
		if err != nil {
			// provide a wrapper to allow require.NoError or ginkgo handling
			goRoutineErrorHandler(fmt.Errorf("error starting main api server: %w", err))
		}
		cancel()
	}()

	// start agent api server
	go func() {
		err := agentServer.Run(ctx)
		if err != nil {
			// provide a wrapper to allow require.NoError or ginkgo handling
			goRoutineErrorHandler(fmt.Errorf("error starting main api server: %w", err))
		}
		cancel()
	}()

	fetchSpecInterval := util.Duration(1 * time.Second)
	statusUpdateInterval := util.Duration(2 * time.Second)

	os.Setenv(agent.TestRootDirEnvKey, testDirPath)
	cfg := agent.NewDefault()
	// TODO: remove the cert/key modifications from default, and start storing
	// the test harness files for those in the testDir/etc/flightctl/certs path
	cfg.EnrollmentService = agent.EnrollmentService{
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
	client, err := testutil.NewClient("https://"+listener.Addr().String(), ca.Config, clientCerts)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	testHarness := &TestHarness{
		agentConfig:           cfg,
		serverListener:        listener,
		goRoutineErrorHandler: goRoutineErrorHandler,
		Context:               ctx,
		cancelCtx:             cancel,
		Server:                server,
		Client:                client,
		Store:                 &store,
		TestDirPath:           testDirPath}

	testHarness.StartAgent()

	return testHarness, nil
}

func (h *TestHarness) Cleanup() {
	h.StopAgent()
	// stop any pending API requests
	h.cancelCtx()
	// unset env var for the test dir path
	os.Unsetenv(agent.TestRootDirEnvKey)
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
	agentInstance := agent.New(agentLog, h.agentConfig)

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
