package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/publisher"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/identity"
	"github.com/flightctl/flightctl/internal/agent/reload"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// TODO: expose via config
	gracefulShutdownTimeout = 2 * time.Minute
)

// New creates a new agent.
func New(log *log.PrefixLogger, config *agent_config.Config, configFile string) *Agent {
	return &Agent{
		config:     config,
		configFile: configFile,
		log:        log,
	}
}

type Agent struct {
	config     *agent_config.Config
	configFile string
	log        *log.PrefixLogger
}

func (a *Agent) GetLogPrefix() string {
	return a.log.Prefix()
}

func (a *Agent) Run(ctx context.Context) error {
	a.log.Infof("Starting agent...")
	defer a.log.Infof("Agent stopped")

	defer utilruntime.HandleCrash()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// create file io writer and reader
	deviceReadWriter := fileio.NewReadWriter(fileio.WithTestRootDir(a.config.GetTestRootDir()))

	tpmClient, err := a.tryLoadTPM(deviceReadWriter)
	if err != nil {
		return fmt.Errorf("failed to initialize TPM client: %w", err)
	}

	// create identity provider
	identityProvider := identity.NewProvider(
		tpmClient,
		deviceReadWriter,
		a.config,
		a.log,
	)

	if err := identityProvider.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize identity provider: %w", err)
	}

	deviceName, err := identityProvider.GetDeviceName()
	if err != nil {
		return fmt.Errorf("failed to get device name: %w", err)
	}

	csr, err := identityProvider.GenerateCSR(deviceName)
	if err != nil {
		return fmt.Errorf("failed to generate CSR: %w", err)
	}

	executer := &executer.CommonExecuter{}

	// create enrollment client
	enrollmentClient, err := newEnrollmentClient(a.config)
	if err != nil {
		return err
	}

	// TODO: replace wait with poll
	backoff := wait.Backoff{
		Cap:      1 * time.Minute,
		Duration: 10 * time.Second,
		Factor:   1.5,
		Steps:    6,
	}

	pollBackoff := poll.Config{
		MaxDelay:  1 * time.Minute,
		BaseDelay: 10 * time.Second,
		Factor:    1.5,
		MaxSteps:  a.config.PullRetrySteps,
	}

	// create os client
	osClient := os.NewClient(a.log, executer)

	// create podman client
	podmanClient := client.NewPodman(a.log, executer, deviceReadWriter, pollBackoff)

	// create systemd client
	systemdClient := client.NewSystemd(executer)

	// create systemInfo manager
	systemInfoManager := systeminfo.NewManager(
		a.log,
		executer,
		deviceReadWriter,
		a.config.DataDir,
		a.config.SystemInfo,
		a.config.SystemInfoCustom,
		a.config.SystemInfoTimeout,
	)
	if err := systemInfoManager.Initialize(ctx); err != nil {
		return err
	}

	// create shutdown manager
	shutdownManager := shutdown.NewManager(a.log, gracefulShutdownTimeout, cancel)

	if tpmClient != nil {
		systemInfoManager.RegisterCollector(ctx, "tpmVendorInfo", tpmClient.VendorInfoCollector)
		shutdownManager.Register("tpm-client", tpmClient.Close)
	}

	reloadManager := reload.NewManager(a.configFile, a.log)

	policyManager := policy.NewManager(a.log)

	devicePublisher := publisher.New(deviceName,
		time.Duration(a.config.SpecFetchInterval),
		backoff,
		a.log)

	// create spec manager
	specManager := spec.NewManager(
		a.config.DataDir,
		policyManager,
		deviceReadWriter,
		osClient,
		devicePublisher.Subscribe(),
		a.log,
	)

	// create resource manager
	resourceManager := resource.NewManager(
		a.log,
	)

	// create hook manager
	hookManager := hook.NewManager(deviceReadWriter, executer, a.log)

	// create application manager
	applicationManager := applications.NewManager(
		a.log,
		deviceReadWriter,
		podmanClient,
		systemInfoManager,
	)

	// register the application manager with the shutdown manager
	shutdownManager.Register("applications", applicationManager.Stop)

	// register identity provider with shutdown manager
	shutdownManager.Register("identity", identityProvider.Close)

	// create systemd manager
	systemdManager := systemd.NewManager(a.log, systemdClient)

	// create os manager
	osManager := os.NewManager(a.log, osClient, deviceReadWriter, podmanClient)

	// create prefetch manager
	prefetchManager := dependency.NewPrefetchManager(a.log, podmanClient, a.config.PullTimeout)

	// create status manager
	statusManager := status.NewManager(
		deviceName,
		a.log,
	)

	// create lifecycle manager
	lifecycleManager := lifecycle.NewManager(
		deviceName,
		a.config.EnrollmentService.EnrollmentUIEndpoint,
		a.config.ManagementService.GetClientCertificatePath(),
		a.config.ManagementService.GetClientKeyPath(),
		deviceReadWriter,
		enrollmentClient,
		csr,
		a.config.DefaultLabels,
		statusManager,
		systemdClient,
		identityProvider,
		backoff,
		a.log,
	)

	// register status exporters
	statusManager.RegisterStatusExporter(applicationManager)
	statusManager.RegisterStatusExporter(systemdManager)
	statusManager.RegisterStatusExporter(resourceManager)
	statusManager.RegisterStatusExporter(osManager)
	statusManager.RegisterStatusExporter(specManager)
	statusManager.RegisterStatusExporter(systemInfoManager)

	// create config controller
	configController := config.NewController(
		deviceReadWriter,
		a.log,
	)

	bootstrap := device.NewBootstrap(
		deviceName,
		executer,
		deviceReadWriter,
		specManager,
		devicePublisher,
		statusManager,
		hookManager,
		lifecycleManager,
		&a.config.ManagementService.Config,
		systemInfoManager,
		a.config.GetManagementMetricsCallback(),
		podmanClient,
		identityProvider,
		a.log,
	)

	// bootstrap
	if err := bootstrap.Initialize(ctx); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// create the gRPC client this must be done after bootstrap
	grpcClient, err := identityProvider.CreateGRPCClient(&a.config.ManagementService.Config)
	if err != nil {
		a.log.Warnf("Failed to create gRPC client: %v", err)
	}

	// create resource controller
	resourceController := resource.NewController(
		a.log,
		resourceManager,
	)

	// create console controller
	consoleController := console.NewController(
		grpcClient,
		deviceName,
		executer,
		devicePublisher.Subscribe(),
		a.log,
	)

	applicationsController := applications.NewController(
		podmanClient,
		applicationManager,
		deviceReadWriter,
		a.log,
	)

	// create agent
	agent := device.NewAgent(
		deviceName,
		deviceReadWriter,
		statusManager,
		specManager,
		devicePublisher,
		applicationManager,
		systemdManager,
		a.config.SpecFetchInterval,
		a.config.StatusUpdateInterval,
		hookManager,
		osManager,
		policyManager,
		lifecycleManager,
		applicationsController,
		configController,
		resourceController,
		consoleController,
		osClient,
		podmanClient,
		prefetchManager,
		backoff,
		a.log,
	)

	// register agent with shutdown manager
	shutdownManager.Register("agent", agent.Stop)

	// register reloader with reload manager
	reloadManager.Register(agent.ReloadConfig)
	reloadManager.Register(systemInfoManager.ReloadConfig)
	reloadManager.Register(statusManager.ReloadCollect)

	go shutdownManager.Run(ctx)
	go reloadManager.Run(ctx)
	go resourceManager.Run(ctx)
	go prefetchManager.Run(ctx)

	return agent.Run(ctx)
}

func newEnrollmentClient(cfg *agent_config.Config) (client.Enrollment, error) {
	httpClient, err := client.NewFromConfig(&cfg.EnrollmentService.Config)
	if err != nil {
		return nil, err
	}
	return client.NewEnrollment(httpClient, cfg.GetEnrollmentMetricsCallback()), nil
}

func (a *Agent) tryLoadTPM(writer fileio.ReadWriter) (*tpm.Client, error) {
	if !a.config.TPM.Enabled {
		a.log.Info("TPM auth is disabled. Skipping TPM setup.")
		return nil, nil
	}

	tpmClient, err := tpm.NewClient(a.log, writer, a.config)
	if err != nil {
		return nil, fmt.Errorf("creating TPM client: %w", err)
	}
	return tpmClient, nil
}
