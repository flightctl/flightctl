package agent

import (
	"context"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
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
	"github.com/flightctl/flightctl/internal/agent/reload"
	"github.com/flightctl/flightctl/internal/agent/shutdown"
	baseconfig "github.com/flightctl/flightctl/internal/config"
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

	// determine device identity source and ensure keys
	publicKey, privateKeySigner, err := newIdentityProvider(a.log, a.config, deviceReadWriter).EnsureIdentity()
	if err != nil {
		return err
	}

	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	if err != nil {
		return err
	}

	deviceName := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash))
	csr, err := fcrypto.MakeCSR(privateKeySigner, deviceName)
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

	// generate tpm client and register collectors if TPM is enabled
	if !a.config.DisableTPM {
		tpmClient, err := lifecycle.NewTpmClient(a.log)
		if err != nil {
			return err
		}
		if err := tpmClient.UpdateNonce(make([]byte, 8)); err != nil {
			a.log.Errorf("Unable to update nonce in tpm client: %v", err)
		}
		systemInfoManager.RegisterCollector(ctx, "tpmVendorInfo", tpmClient.TpmVendorInfoCollector)
		systemInfoManager.RegisterCollector(ctx, "attestation", tpmClient.TpmAttestationCollector)
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
