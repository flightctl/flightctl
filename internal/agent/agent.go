package agent

import (
	"context"
	"crypto"
	"encoding/base32"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager"
	cm_config "github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/config"
	cm_provisioner "github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/provisioner"
	cm_state "github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/state"
	cm_storage "github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/storage"
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
	"github.com/flightctl/flightctl/internal/agent/reload"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	baseconfig "github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/experimental"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
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

	// ensure the agent key exists if not create it.
	if !a.config.ManagementService.Config.HasCredentials() {
		a.config.ManagementService.Config.AuthInfo.ClientCertificate = filepath.Join(a.config.DataDir, agent_config.DefaultCertsDirName, agent_config.GeneratedCertFile)
		a.config.ManagementService.Config.AuthInfo.ClientKey = filepath.Join(a.config.DataDir, agent_config.DefaultCertsDirName, agent_config.KeyFile)
	}
	publicKey, privateKey, _, err := fcrypto.EnsureKey(deviceReadWriter.PathFor(a.config.ManagementService.AuthInfo.ClientKey))
	if err != nil {
		return err
	}

	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	if err != nil {
		return err
	}

	deviceName := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash))
	csr, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), deviceName)
	if err != nil {
		return err
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

	// generate tpm client only if experimental features are enabled
	experimentalFeatures := experimental.NewFeatures()
	if experimentalFeatures.IsEnabled() {
		a.log.Warn("Experimental features enabled: creating TPM client")
		tpmClient, err := lifecycle.NewTpmClient(a.log)
		if err != nil {
			a.log.Warnf("Experimental feature: tpm is not available: %v", err)
		} else {
			a.log.Warn("Experimental features enabled: registering TPM info collection functions")
			err := tpmClient.UpdateNonce(make([]byte, 8))
			if err != nil {
				a.log.Errorf("Unable to update nonce in tpm client: %v", err)
			}
			systemInfoManager.RegisterCollector(ctx, "tpmVendorInfo", tpmClient.TpmVendorInfoCollector)
			systemInfoManager.RegisterCollector(ctx, "attestation", tpmClient.TpmAttestationCollector)
		}
	} else {
		a.log.Debug("Experimental features are not enabled: skipping creation of TPM client and registration of TPM collection functions")
	}

	// create shutdown manager
	shutdownManager := shutdown.NewManager(a.log, gracefulShutdownTimeout, cancel)

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
		a.log,
	)

	// bootstrap
	if err := bootstrap.Initialize(ctx); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// Initialize certificate
	certManager, err := certmanager.NewManager(a.log,
		certmanager.WithStateStorageProvider(cm_state.NewFileStorage(filepath.Join(agent_config.DefaultConfigDir, "cert-state.json"))),
		certmanager.WithConfigProvider(cm_config.NewAgentConfigProvider(ctx, a.configFile)),
		certmanager.WithProvisionerProvider(cm_provisioner.NewCSRProvisionerFactory(deviceName, bootstrap.ManagementClient())),
		certmanager.WithStorageProvider(cm_storage.NewFileSystemStorageFactory(deviceReadWriter)),

		// Empty providers for testing and placeholder scenarios
		certmanager.WithProvisionerProvider(cm_provisioner.NewEmptyProvisionerFactory()),
		certmanager.WithStorageProvider(cm_storage.NewEmptyStorageFactory()),
	)
	if err != nil {
		return fmt.Errorf("certificate manager initialization failed: %w", err)
	}

	// create the gRPC client this must be done after bootstrap
	grpcClient, err := newGrpcClient(&a.config.ManagementService)
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
	go certManager.Run(ctx)

	return agent.Run(ctx)
}

func newEnrollmentClient(cfg *agent_config.Config) (client.Enrollment, error) {
	httpClient, err := client.NewFromConfig(&cfg.EnrollmentService.Config)
	if err != nil {
		return nil, err
	}
	return client.NewEnrollment(httpClient, cfg.GetEnrollmentMetricsCallback()), nil
}

func newGrpcClient(cfg *baseconfig.ManagementService) (grpc_v1.RouterServiceClient, error) {
	client, err := client.NewGRPCClientFromConfig(&cfg.Config)
	if err != nil {
		return nil, fmt.Errorf("creating gRPC client: %w", err)
	}
	return client, nil
}
