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
	"github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
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
	Client      *client.ClientWithResponses
	Store       *store.Store
	TestDirPath string
}

// NewTestHarness creates a new test harness and returns a new test harness
// The test harness can be used from testing code to interact with a
// set of agent/server/store instances.
// It provides the necessary elements to perform tests against the agent and server.
func NewTestHarness(testDirPath string, goRoutineErrorHandler func(error)) (*TestHarness, error) {

	err := makeTestDirs(testDirPath, []string{"/etc/issue.d/"})
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness failed creating temporary directories: %w", err)
	}

	serverCfg := *config.NewDefault()
	serverLog := log.InitLogs()

	// create store
	store, dbName, err := testutil.NewTestStore(serverCfg, serverLog)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}
	serverCfg.Database.Name = dbName

	// create certs
	serverCfg.Service.CertStore = testDirPath
	ca, serverCerts, clientCerts, err := testutil.NewTestCerts(&serverCfg)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	// create server
	server, listener, err := testutil.NewTestServer(serverLog, &serverCfg, store, ca, serverCerts)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}
	serverCfg.Service.Address = listener.Addr().String()

	// start server
	go func() {
		err := server.Run()
		if err != nil {
			// provide a wrapper to allow require.NoError or ginkgo handling
			goRoutineErrorHandler(fmt.Errorf("error starting server: %w", err))
		}
	}()

	fetchSpecInterval := 1 * time.Second
	statusUpdateInterval := 1 * time.Second

	cfg := agent.NewDefault()
	// TODO: remove the cert/key modifications from default, and start storing
	// the test harness files for those in the testDir/etc/flightctl/certs path
	cfg.Cacert = "ca.crt"
	cfg.EnrollmentCertFile = "client-enrollment.crt"
	cfg.EnrollmentKeyFile = "client-enrollment.key"
	cfg.EnrollmentEndpoint = "https://" + serverCfg.Service.Address
	cfg.EnrollmentUIEndpoint = "https://flightctl.ui/"
	cfg.ManagementEndpoint = "https://" + serverCfg.Service.Address
	cfg.SpecFetchInterval = fetchSpecInterval
	cfg.StatusUpdateInterval = statusUpdateInterval
	cfg.SetTestRootDir(testDirPath)

	// create client to talk to the server
	client, err := testutil.NewClient("https://"+listener.Addr().String(), ca.Config, clientCerts)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

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
	// stop the server
	h.serverListener.Close()
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
	agentLog := log.InitLogs()
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
