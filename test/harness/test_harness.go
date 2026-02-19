package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	workerserver "github.com/flightctl/flightctl/internal/worker_server"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
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
	Context        context.Context
	Server         *apiserver.Server
	Agent          *agent.Agent
	Client         *apiclient.ClientWithResponses
	Store          *store.Store
	ServiceHandler service.Service // Service handler for direct service calls (bypasses HTTP/auth)
	TestDirPath    string
}

type TestHarnessOption func(h *TestHarness)

// createAdminIdentity creates a test admin identity with full permissions
func createAdminIdentity() *identity.MappedIdentity {
	testOrg := &model.Organization{
		ID:          org.DefaultID,
		ExternalID:  "default",
		DisplayName: "Default Organization",
	}
	return &identity.MappedIdentity{
		Username:      "test-admin",
		UID:           uuid.New().String(),
		Organizations: []*model.Organization{testOrg},
		OrgRoles:      map[string][]string{"*": {string(api.RoleAdmin)}},
		SuperAdmin:    true,
	}
}

func createAdminBaseIdentity() *common.BaseIdentity {
	testOrg := common.ReportedOrganization{
		Name:         "default",
		IsInternalID: true,
		ID:           org.DefaultID.String(),
		Roles:        []string{string(api.RoleAdmin)},
	}
	return common.NewBaseIdentity("test-admin", uuid.New().String(), []common.ReportedOrganization{testOrg})
}

// WithAgentMetrics enables the agent's Prometheus metrics endpoint when the
// harness starts the agent.
func WithAgentMetrics() TestHarnessOption {
	return func(h *TestHarness) {
		if h.agentConfig != nil {
			h.agentConfig.MetricsEnabled = true
		}
	}
}

// WithAgentPprof enables the agent's pprof HTTP server when the harness starts
// the agent.
func WithAgentPprof() TestHarnessOption {
	return func(h *TestHarness) {
		if h.agentConfig != nil {
			h.agentConfig.ProfilingEnabled = true
		}
	}
}

// WithAgentAudit enables the agent's audit logging when the harness starts the agent.
// Note: Audit logging is enabled by default, so this option is primarily for test clarity.
func WithAgentAudit() TestHarnessOption {
	return func(h *TestHarness) {
		if h.agentConfig != nil {
			enabled := true
			h.agentConfig.AuditLog.Enabled = &enabled
		}
	}
}

// NewTestHarness creates a new test harness and returns a new test harness
// The test harness can be used from testing code to interact with a
// set of agent/server/store instances.
// It provides the necessary elements to perform tests against the agent and server.
func NewTestHarness(ctx context.Context, testDirPath string, goRoutineErrorHandler func(error), opts ...TestHarnessOption) (*TestHarness, error) {
	err := makeTestDirs(testDirPath, []string{"/etc/flightctl/certs", "/etc/issue.d/", "/var/lib/flightctl/", "/proc/net"})
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness failed creating temporary directories: %w", err)
	}
	if err = addRouteTable(testDirPath); err != nil {
		return nil, fmt.Errorf("NewTestHarness failed adding mock route table: %w", err)
	}

	serverCfg := *config.NewDefault()
	serverLog := log.InitLogs("debug")
	serverLog.SetOutput(os.Stdout)

	// create store
	store, dbName, err := testutil.NewTestStore(ctx, serverCfg, serverLog)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}
	serverCfg.Database.Name = dbName

	// create certs
	serverCfg.Service.CertStore = filepath.Join(testDirPath, "etc", "flightctl", "certs")
	serverCfg.CA.InternalConfig.CertStore = serverCfg.Service.CertStore
	ca, serverCerts, _, err := testutil.NewTestCerts(&serverCfg)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	// Add admin identity to context for service calls
	ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, createAdminIdentity())

	provider := testutil.NewTestProvider(serverLog)
	// create server
	apiServer, listener, err := testutil.NewTestApiServer(serverLog, &serverCfg, store, ca, serverCerts, provider)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	ctrl := gomock.NewController(GinkgoT())
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	workerServer := workerserver.New(&serverCfg, serverLog, store, provider, mockK8sClient, nil)

	agentServer, agentListener, err := testutil.NewTestAgentServer(ctx, serverLog, &serverCfg, store, ca, serverCerts, provider)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	serverCfg.Service.Address = listener.Addr().String()
	serverCfg.Service.AgentEndpointAddress = agentListener.Addr().String()

	// start main api server
	go func() {
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
	cfg.EnrollmentService = config.EnrollmentService{
		Config:               *client.NewDefault(),
		EnrollmentUIEndpoint: "https://flightctl.ui/",
	}
	cfg.EnrollmentService.Service = client.Service{
		Server:               "https://" + serverCfg.Service.AgentEndpointAddress,
		CertificateAuthority: "/etc/flightctl/certs/client-signer.crt",
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

	// Create service handler for direct service calls (bypassing HTTP/auth middleware)
	kvStore, err := kvstore.NewKVStore(ctx, serverLog, serverCfg.KV.Hostname, serverCfg.KV.Port, serverCfg.KV.Password)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: failed to create KV store: %w", err)
	}
	publisher, err := worker_client.QueuePublisher(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: failed to create queue publisher: %w", err)
	}
	workerClient := worker_client.NewWorkerClient(publisher, serverLog)
	serviceHandler := service.NewServiceHandler(store, workerClient, kvStore, ca, serverLog, "", "", []string{}, nil)

	testHarness := &TestHarness{
		agentConfig:           cfg,
		serverListener:        listener,
		goRoutineErrorHandler: goRoutineErrorHandler,
		Context:               ctx,
		cancelCtx:             cancel,
		Server:                apiServer,
		Client:                client,
		Store:                 &store,
		ServiceHandler:        serviceHandler,
		mockK8sClient:         mockK8sClient,
		ctrl:                  ctrl,
		TestDirPath:           testDirPath}

	// Apply test harness options before starting the agent
	for _, o := range opts {
		if o != nil {
			o(testHarness)
		}
	}

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

// AuthenticatedContext adds admin identities and org ID to the provided context for direct service calls
// Use this when calling ServiceHandler methods to bypass HTTP/auth middleware
func (h *TestHarness) AuthenticatedContext(ctx context.Context) context.Context {
	// Add both Identity and MappedIdentity to context for completeness
	ctx = context.WithValue(ctx, consts.IdentityCtxKey, createAdminBaseIdentity())
	ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, createAdminIdentity())
	// Also add org ID to context so it's available for signing certificates
	ctx = util.WithOrganizationID(ctx, org.DefaultID)
	return ctx
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

// addRouteTable copies the ipv4 route table from the root directory to the new test directory.
// currently agents wait for routes to become available using the routes provided in their directory.
// They'll poll for 45 seconds before giving up an allowing onboarding to complete
func addRouteTable(testDirPath string) error {
	routeTable := filepath.Join("proc", "net", "route")
	dst := filepath.Join(testDirPath, routeTable)
	src, err := os.Open(filepath.Join("/", routeTable))
	if err != nil {
		return fmt.Errorf("failed to open system route table %s: %w", routeTable, err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat system route table %s: %w", routeTable, err)
	}

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create copy of system route table %s: %w", routeTable, err)
	}
	defer out.Close()

	if _, err = io.Copy(out, src); err != nil {
		return fmt.Errorf("failed to copy system route table %s: %w", routeTable, err)
	}
	if err = os.Chmod(dst, info.Mode()); err != nil {
		return fmt.Errorf("failed to chmod system route table %s: %w", routeTable, err)
	}
	return nil
}
