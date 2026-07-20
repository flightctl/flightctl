package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
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
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/rendered"
	certificatesigningrequestservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/store"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	enrollmentrequeststore "github.com/flightctl/flightctl/internal/store/enrollmentrequest"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	workerserver "github.com/flightctl/flightctl/internal/worker_server"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/integration/integrationstack"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	"go.uber.org/mock/gomock"
)

type TestHarness struct {
	// internals for agent
	cancelAgentCtx context.CancelFunc
	agentFinished  chan struct{}
	agentConfig    *agent_config.Config
	skipAutoStart  bool

	// internals for server
	serverListener  net.Listener
	serversFinished chan struct{}

	// error handler for go routines
	goRoutineErrorHandler func(error)

	// internals for client context
	cancelCtx context.CancelFunc

	// K8S client mock
	mockK8sClient *k8sclient.MockK8SClient
	ctrl          *gomock.Controller

	vulnerabilityEnabled bool

	// Redis connection params (must be set via WithRedis for ephemeral Redis isolation)
	redisHost     string
	redisPort     uint
	redisPassword domain.SecureString

	// attributes for the test harness
	Context     context.Context
	Server      *apiserver.Server
	Agent       *agent.Agent
	Client      *apiclient.ClientWithResponses
	DeviceStore devicestore.Store

	// Focused service handlers for direct service calls (bypasses HTTP/auth). Only the
	// resources actually exercised by harness consumers are exposed here - see
	// test/integration/agent/agent_test.go for the full set of methods used.
	Device                    deviceservice.Service
	EnrollmentRequest         enrollmentrequestservice.Service
	Fleet                     fleetservice.Service
	CertificateSigningRequest certificatesigningrequestservice.Service

	KVStore     kvstore.KVStore // Same Redis as service; use to set awaiting-reconnect / rendered keys for tests
	TestDirPath string
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

// WithVulnerabilityEnabled enables the vulnerability feature endpoints for the
// service handler created by NewTestHarness.
func WithVulnerabilityEnabled() TestHarnessOption {
	return func(h *TestHarness) {
		h.vulnerabilityEnabled = true
	}
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

func WithoutAutoStartAgent() TestHarnessOption {
	return func(h *TestHarness) {
		h.skipAutoStart = true
	}
}

// WithRedis sets the Redis connection parameters for the test harness.
// This is required for parallel test execution where each suite has its own ephemeral Redis.
// Call testdb.CreateTestRedis() in BeforeSuite and pass the connection params here.
func WithRedis(host string, port uint, password domain.SecureString) TestHarnessOption {
	return func(h *TestHarness) {
		h.redisHost = host
		h.redisPort = port
		h.redisPassword = password
	}
}

// NewTestHarness creates a new test harness and returns a new test harness
// The test harness can be used from testing code to interact with a
// set of agent/server/store instances.
// It provides the necessary elements to perform tests against the agent and server.
// IMPORTANT: For parallel test execution, you MUST pass WithRedis() with connection params
// from testdb.CreateTestRedis() called in your suite's BeforeSuite.
func NewTestHarness(ctx context.Context, testDirPath string, goRoutineErrorHandler func(error), opts ...TestHarnessOption) (*TestHarness, error) {
	err := makeTestDirs(testDirPath, []string{"/etc/flightctl/certs", "/etc/issue.d/", "/var/lib/flightctl/", "/proc/net"})
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness failed creating temporary directories: %w", err)
	}
	if err = addRouteTable(testDirPath); err != nil {
		return nil, fmt.Errorf("NewTestHarness failed adding mock route table: %w", err)
	}

	if err := integrationstack.EnsureRunning(ctx); err != nil {
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	// Create a temporary harness to collect options (we need Redis params before creating KV store)
	tempHarness := &TestHarness{}
	for _, o := range opts {
		if o != nil {
			o(tempHarness)
		}
	}

	// Validate that Redis params were provided (required for parallel test isolation)
	if tempHarness.redisHost == "" || tempHarness.redisPort == 0 {
		return nil, fmt.Errorf("NewTestHarness: Redis connection params required - call testdb.CreateTestRedis() in BeforeSuite and pass WithRedis(host, port, password)")
	}

	serverCfg := *config.NewDefault()
	testdb.ApplyIntegrationConnectionOverrides(&serverCfg)
	serverLog := log.InitLogs("debug")
	serverLog.SetOutput(os.Stdout)

	// create store using template cloning (faster than running migrations)
	_, dbName, db, err := testdb.CreateTestDB(ctx, serverLog, "", store.InitDB)
	if err != nil {
		return nil, fmt.Errorf("NewTestHarness: CreateTestDB: %w", err)
	}
	deviceStore := devicestore.NewDeviceStore(db, serverLog.WithField("pkg", "device-store"))
	fleetStore := fleetstore.NewFleetStore(db, serverLog.WithField("pkg", "fleet-store"))
	enrollmentRequestStore := enrollmentrequeststore.NewEnrollmentRequestStore(db, serverLog.WithField("pkg", "enrollmentrequest-store"))
	csrStore := certificatesigningrequeststore.NewCertificateSigningRequestStore(db, serverLog.WithField("pkg", "csr-store"))
	eventStore := eventstore.NewEventStore(db, serverLog.WithField("pkg", "event-store"))
	serverCfg.Database.Name = dbName

	// Apply Redis params from options to server config
	serverCfg.KV.Hostname = tempHarness.redisHost
	serverCfg.KV.Port = tempHarness.redisPort
	serverCfg.KV.Password = tempHarness.redisPassword

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

	// Create KV store using Redis params from options (ephemeral per-suite Redis)
	kvStore, err := kvstore.NewKVStore(ctx, serverLog, serverCfg.KV.Hostname, serverCfg.KV.Port, serverCfg.KV.Password)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("NewTestHarness: failed to create KV store: %w", err)
	}
	// Use a short rendered wait timeout so the ConflictPaused test is stable: when the agent is
	// already in GetRenderedDevice (blocked in WaitForNewVersion) as the test sets the KV key,
	// that request returns after this timeout; the agent's next poll (minPollDelay 5s later) then
	// sees the key and gets 200+ConflictPaused. With 2s we get 2+5+2 ≤ 10s within the test timeout.
	if err := rendered.Bus.Initialize(ctx, kvStore, provider, 2*time.Second, serverLog); err != nil {
		kvStore.Close()
		cancel()
		return nil, fmt.Errorf("NewTestHarness: failed to initialize rendered bus: %w", err)
	}

	// create server
	apiServer, listener, err := testutil.NewTestApiServer(serverLog, &serverCfg, db, ca, serverCerts, provider)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	ctrl := gomock.NewController(GinkgoT())
	mockK8sClient := k8sclient.NewMockK8SClient(ctrl)
	workerServer := workerserver.New(&serverCfg, serverLog, db, provider, mockK8sClient, nil)

	agentServer, agentListener, err := testutil.NewTestAgentServer(ctx, serverLog, &serverCfg, db, ca, serverCerts, provider)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	serverCfg.Service.Address = listener.Addr().String()
	serverCfg.Service.AgentEndpointAddress = agentListener.Addr().String()

	// Track when all servers have finished for proper cleanup
	var serversWg sync.WaitGroup
	serversFinished := make(chan struct{})

	// start main api server
	serversWg.Add(1)
	go func() {
		defer serversWg.Done()
		err := apiServer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			goRoutineErrorHandler(fmt.Errorf("error starting main api server: %w", err))
			cancel() // cascade failure to other servers
		}
	}()

	serversWg.Add(1)
	go func() {
		defer serversWg.Done()
		err := workerServer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			goRoutineErrorHandler(fmt.Errorf("error starting worker server: %w", err))
			cancel() // cascade failure to other servers
		}
	}()

	// start agent api server
	serversWg.Add(1)
	go func() {
		defer serversWg.Done()
		err := agentServer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			goRoutineErrorHandler(fmt.Errorf("error starting main agent api server: %w", err))
			cancel() // cascade failure to other servers
		}
	}()

	// Close serversFinished when all servers exit
	go func() {
		serversWg.Wait()
		close(serversFinished)
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
		cancel()
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		cancel()
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	// create client to talk to the server
	client, err := testutil.NewClient("https://"+listener.Addr().String(), ca.GetCABundleX509())
	if err != nil {
		cancel()
		return nil, fmt.Errorf("NewTestHarness: %w", err)
	}

	testHarness := &TestHarness{
		agentConfig:           cfg,
		serverListener:        listener,
		serversFinished:       serversFinished,
		goRoutineErrorHandler: goRoutineErrorHandler,
		Context:               ctx,
		cancelCtx:             cancel,
		Server:                apiServer,
		Client:                client,
		DeviceStore:           deviceStore,
		KVStore:               kvStore,
		mockK8sClient:         mockK8sClient,
		ctrl:                  ctrl,
		TestDirPath:           testDirPath,
		// Copy option values from tempHarness (options were already applied above)
		vulnerabilityEnabled: tempHarness.vulnerabilityEnabled,
		skipAutoStart:        tempHarness.skipAutoStart,
		redisHost:            tempHarness.redisHost,
		redisPort:            tempHarness.redisPort,
		redisPassword:        tempHarness.redisPassword,
	}

	// Re-apply options that modify agentConfig (tempHarness.agentConfig is nil since agentConfig is created later)
	for _, o := range opts {
		if o != nil {
			o(testHarness)
		}
	}

	// Create service handler for direct service calls (bypassing HTTP/auth middleware)
	publisher, err := worker_client.QueuePublisher(ctx, provider)
	if err != nil {
		kvStore.Close()
		cancel()
		ctrl.Finish()
		return nil, fmt.Errorf("NewTestHarness: failed to create queue publisher: %w", err)
	}
	workerClient := worker_client.NewWorkerClient(publisher, serverLog)
	eventsSvc := events.NewServiceHandler(eventStore, workerClient, serverLog)
	testHarness.Device = deviceservice.NewDeviceServiceHandler(deviceStore, fleetStore, eventsSvc, kvStore, "", serverLog)
	testHarness.Fleet = fleetservice.NewServiceHandler(fleetStore, eventsSvc, serverLog)
	testHarness.EnrollmentRequest = enrollmentrequestservice.NewServiceHandler(enrollmentRequestStore, deviceStore, csrStore, ca, kvStore, eventsSvc, serverLog, []string{}, "", "")
	testHarness.CertificateSigningRequest = certificatesigningrequestservice.NewServiceHandler(csrStore, enrollmentRequestStore, ca, eventsSvc, serverLog, "", "")

	// Only auto-start agent if not explicitly disabled via WithoutAutoStartAgent()
	if !testHarness.skipAutoStart {
		testHarness.StartAgent()
	}

	return testHarness, nil
}

func (h *TestHarness) Cleanup() {
	h.StopAgent()
	// stop any pending API requests
	h.cancelCtx()
	// wait for all servers to finish gracefully with timeout
	if h.serversFinished != nil {
		select {
		case <-h.serversFinished:
			// servers shut down gracefully
		case <-time.After(30 * time.Second):
			fmt.Fprintf(os.Stderr, "WARNING: test harness servers did not shut down within 30s, proceeding with cleanup\n")
		}
	}
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
	if h.cancelAgentCtx == nil || h.agentFinished == nil {
		return
	}
	h.cancelAgentCtx()
	<-h.agentFinished
	h.cancelAgentCtx = nil
	h.agentFinished = nil
}

func (h *TestHarness) StartAgent() {
	agentLog := log.NewPrefixLogger("")

	// Use SafeExecuter in tests to prevent dangerous commands like systemctl reboot
	safeExec := testutil.NewDefaultSafeExecuter()
	agentInstance := agent.New(agentLog, h.agentConfig, "", agent.WithExecuter(safeExec))

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

// AgentConfig returns the agent configuration for test customization
func (h *TestHarness) AgentConfig() *agent_config.Config {
	return h.agentConfig
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
