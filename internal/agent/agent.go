package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/spec/audit"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/identity"
	"github.com/flightctl/flightctl/internal/agent/instrumentation"
	"github.com/flightctl/flightctl/internal/agent/reload"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/version"
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

	// start instrumentation early so startup paths are observable.
	go instrumentation.NewAgentInstrumentation(a.log, a.config).Run(ctx)

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

	clientCSRPath := identity.GetCSRPath(a.config.DataDir)

	// Try to load persisted CSR first, generate a new one only if not found
	csr, found, err := identity.LoadCSR(deviceReadWriter, clientCSRPath)
	if err != nil {
		return fmt.Errorf("failed to load CSR: %w", err)
	}

	if !found {
		a.log.Infof("No persisted CSR found, generating new CSR for enrollment")
		csr, err = identityProvider.GenerateCSR(deviceName)
		if err != nil {
			return fmt.Errorf("failed to generate CSR: %w", err)
		}

		if err := identity.StoreCSR(deviceReadWriter, clientCSRPath, csr); err != nil {
			return fmt.Errorf("failed to store CSR: %w", err)
		}
		a.log.Infof("CSR generated and persisted successfully")
	} else {
		a.log.Infof("Using persisted CSR for enrollment")
	}

	executer := &executer.CommonExecuter{}

	// create enrollment client
	enrollmentClient, err := newEnrollmentClient(a.config, a.log)
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
		MaxDelay:     1 * time.Minute,
		BaseDelay:    10 * time.Second,
		Factor:       1.5,
		MaxSteps:     a.config.PullRetrySteps,
		JitterFactor: 0.1,
	}

	// create os client
	osClient := os.NewClient(a.log, executer)

	// create podman client
	podmanClient := client.NewPodman(a.log, executer, deviceReadWriter, pollBackoff)

	// create skopeo client
	skopeoClient := client.NewSkopeo(a.log, executer, deviceReadWriter)

	// create systemd client
	systemdClient := client.NewSystemd(executer)

	// create journalctl client
	journalctlClient := client.NewJournalctl(executer)

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
	shutdownManager := shutdown.NewManager(a.log, systemdClient, deviceReadWriter, gracefulShutdownTimeout, cancel)

	if tpmClient != nil {
		systemInfoManager.RegisterCollector(ctx, "tpmVendorInfo", tpmClient.VendorInfoCollector)
		defer func() {
			if err = tpmClient.Close(); err != nil {
				a.log.Errorf("Failed to close TPM client: %v", err)
			}
		}()
	}

	reloadManager := reload.NewManager(a.configFile, a.log)

	policyManager := policy.NewManager(a.log)

	deviceNotFoundHandler := func() error {
		return wipeCertificateAndRestart(ctx, identityProvider, executer, a.log)
	}

	// create audit logger
	auditLogger, err := audit.NewFileLogger(
		&a.config.AuditLog,
		deviceReadWriter,
		deviceName,
		version.Get().String(),
		a.log,
	)
	if err != nil {
		return fmt.Errorf("failed to create audit logger: %w", err)
	}
	defer func() {
		if err := auditLogger.Close(); err != nil {
			a.log.Errorf("Failed to close audit logger: %v", err)
		}
	}()

	// create spec manager
	specManager := spec.NewManager(
		deviceName,
		a.config.DataDir,
		policyManager,
		deviceReadWriter,
		osClient,
		pollBackoff,
		deviceNotFoundHandler,
		auditLogger,
		a.log,
	)

	// create resource manager
	resourceManager := resource.NewManager(
		a.log,
	)

	// create hook manager
	hookManager := hook.NewManager(deviceReadWriter, executer, a.log)

	// create systemd manager
	systemdManager := systemd.NewManager(a.log, systemdClient, journalctlClient)

	// create application manager
	applicationsManager := applications.NewManager(
		a.log,
		deviceReadWriter,
		podmanClient,
		systemInfoManager,
		systemdManager,
	)

	// register the application manager with the shutdown manager
	shutdownManager.Register("applications", applicationsManager.Shutdown)

	// create os manager
	osManager := os.NewManager(a.log, osClient, deviceReadWriter, podmanClient)

	// create prefetch manager
	prefetchManager := dependency.NewPrefetchManager(
		a.log, podmanClient,
		skopeoClient,
		deviceReadWriter,
		a.config.PullTimeout,
		resourceManager,
		pollBackoff,
	)

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
		a.config.DataDir,
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
	statusManager.RegisterStatusExporter(applicationsManager)
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

	// Initialize certificate manager
	certManager, err := certmanager.NewManager(
		ctx, a.log,
		certmanager.WithBuiltins(
			deviceName,
			bootstrap.ManagementClient(),
			deviceReadWriter,
			a.config,
			identity.NewExportableFactory(tpmClient, a.log),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to initialize certificate manager: %w", err)
	}

	if err := certManager.Sync(ctx, a.config); err != nil {
		a.log.Warnf("Failed to sync certificate manager: %v", err)
	}

	// create the gRPC client this must be done after bootstrap
	grpcClient, err := identityProvider.CreateGRPCClient(&a.config.ManagementService.Config)
	if err != nil {
		a.log.Warnf("Failed to create gRPC client: %v", err)
	}

	// create console manager
	consoleManager := console.NewManager(
		grpcClient,
		deviceName,
		executer,
		specManager.Watch(),
		a.log,
	)

	applicationsController := applications.NewController(
		podmanClient,
		applicationsManager,
		deviceReadWriter,
		a.log,
		systemInfoManager.BootTime(),
	)

	// create agent
	agent := device.NewAgent(
		deviceName,
		deviceReadWriter,
		statusManager,
		specManager,
		applicationsManager,
		systemdManager,
		a.config.StatusUpdateInterval,
		hookManager,
		osManager,
		policyManager,
		lifecycleManager,
		applicationsController,
		configController,
		resourceManager,
		consoleManager,
		osClient,
		podmanClient,
		prefetchManager,
		backoff,
		a.log,
	)

	// register reloader with reload manager
	reloadManager.Register(agent.ReloadConfig)
	reloadManager.Register(systemInfoManager.ReloadConfig)
	reloadManager.Register(statusManager.ReloadCollect)
	reloadManager.Register(certManager.Sync)

	// agent is serial by default. only a small number of operations run async.
	// device reconciliation, status updates, and spec application happen serially.

	// async to handle OS signals (SIGTERM, SIGINT) for graceful shutdown
	go shutdownManager.Run(ctx)

	// async to watch config file changes without blocking reconciliation
	go reloadManager.Run(ctx)

	// async monitors for various resources (cpu, memory, disk)
	// monitoring must not be blocked by other operations to detect resource changes promptly
	go resourceManager.Run(ctx)

	// async to pre-pull container images without blocking the main loop
	// image pulls can take minutes and should not delay device reconciliation
	go prefetchManager.Run(ctx)

	// async to handle bidirectional gRPC streams for remote shell access
	// must run independently to maintain persistent console connections
	go consoleManager.Run(ctx)

	// publisher is async to poll management server for spec updates at regular intervals
	// fetching is async, but spec management remains serial in the main agent loop
	go specManager.Publisher().Run(ctx)

	// main agent loop: all critical work happens here serially
	return agent.Run(ctx)
}

func newEnrollmentClient(cfg *agent_config.Config, log *log.PrefixLogger) (client.Enrollment, error) {
	// Create infinite retry policy for enrollment requests using same config as management client
	// but with infinite retries (MaxSteps: 0)
	infiniteRetryConfig := poll.Config{
		BaseDelay:    10 * time.Second,
		Factor:       1.5,
		MaxDelay:     1 * time.Minute,
		MaxSteps:     0,
		JitterFactor: 0.1,
	}

	httpClient, err := client.NewFromConfig(&cfg.EnrollmentService.Config, log, client.WithHTTPRetry(infiniteRetryConfig))
	if err != nil {
		return nil, err
	}
	return client.NewEnrollment(httpClient, cfg.GetEnrollmentMetricsCallback()), nil
}

func (a *Agent) tryLoadTPM(writer fileio.ReadWriter) (tpm.Client, error) {
	if !a.config.TPM.Enabled {
		a.log.Info("TPM device identity is disabled. Skipping TPM setup.")
		return nil, nil
	}

	tpmClient, err := tpm.NewClient(a.log, writer, a.config)
	if err != nil {
		return nil, fmt.Errorf("creating TPM client: %w", err)
	}
	return tpmClient, nil
}

// wipeCertificateAndRestart wipes only the certificate (not keys or CSR) and restarts the flightctl-agent service
func wipeCertificateAndRestart(ctx context.Context, identityProvider identity.Provider, executer executer.Executer, log *log.PrefixLogger) error {
	log.Warn("Device not found on management server - wiping certificate and restarting agent")

	// Wipe only the certificate, preserving keys and CSR
	if err := identityProvider.WipeCertificateOnly(); err != nil {
		return fmt.Errorf("failed to wipe certificate: %w", err)
	}

	// Restart the flightctl-agent service
	systemdClient := client.NewSystemd(executer)
	if err := systemdClient.Restart(ctx, "flightctl-agent"); err != nil {
		return fmt.Errorf("failed to restart flightctl-agent service: %w", err)
	}

	log.Info("Successfully wiped certificate and restarted flightctl-agent service")
	return nil
}
