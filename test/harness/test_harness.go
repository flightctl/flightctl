package harness

import (
	"context"
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
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
)

type TestHarness struct {
	// internals for cleanup
	cancelCtx      context.CancelFunc
	serverListener net.Listener

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
	cfg.CertDir = testDirPath
	cfg.EnrollmentServerEndpoint = "https://" + serverCfg.Service.Address
	cfg.EnrollmentUIEndpoint = "https://flightctl.ui/"
	cfg.ManagementServerEndpoint = "https://" + serverCfg.Service.Address
	cfg.FetchSpecInterval = util.Duration(fetchSpecInterval)
	cfg.StatusUpdateInterval = util.Duration(statusUpdateInterval)
	cfg.SetTestRootDir(testDirPath)

	// create client to talk to the server
	client, err := testutil.NewClient("https://"+listener.Addr().String(), ca.Config, clientCerts)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	agentLog := log.InitLogs()
	agentInstance := agent.New(agentLog, cfg)

	ctx, cancel := context.WithCancel(context.Background())

	// start agent
	go func() {
		err := agentInstance.Run(ctx)
		if err != nil {
			goRoutineErrorHandler(fmt.Errorf("error starting agent: %w", err))
		}
	}()

	return &TestHarness{
		cancelCtx:      cancel,
		serverListener: listener,
		Context:        ctx,
		Server:         server,
		Client:         client,
		Store:          &store,
		Agent:          agentInstance,
		TestDirPath:    testDirPath}, nil
}

func (h *TestHarness) Cleanup() {
	// stop the agent
	h.cancelCtx()
	// stop the server
	h.serverListener.Close()
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
