package agent

import (
	"context"
	"crypto"
	"encoding/base32"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

// New creates a new agent.
func New(log *log.PrefixLogger, config *Config) *Agent {
	return &Agent{
		config: config,
		log:    log,
	}
}

type Agent struct {
	config *Config
	log    *log.PrefixLogger
}

func (a *Agent) GetLogPrefix() string {
	return a.log.Prefix()
}

func (a *Agent) Run(ctx context.Context) error {
	a.log.Infof("Starting agent...")
	defer a.log.Infof("Agent stopped")

	defer utilruntime.HandleCrash()
	ctx, cancel := context.WithCancel(ctx)
	shutdownSignals := []os.Signal{os.Interrupt, syscall.SIGTERM}

	// handle teardown
	shutdownHandler := make(chan os.Signal, 2)
	signal.Notify(shutdownHandler, shutdownSignals...)
	go func(ctx context.Context) {
		select {
		case <-shutdownHandler:
			a.log.Infof("Received SIGTERM or SIGINT signal, shutting down.")
			close(shutdownHandler)
			cancel()
		case <-ctx.Done():
			a.log.Infof("Context has been cancelled, shutting down.")
			close(shutdownHandler)
			cancel()
		}
	}(ctx)

	// create file io writer and reader
	deviceWriter, deviceReader := initializeFileIO(a.config)

	currentSpecFilePath := filepath.Join(a.config.DataDir, spec.CurrentFile)
	desiredSpecFilePath := filepath.Join(a.config.DataDir, spec.DesiredFile)

	// ensure the agent key exists if not create it.
	if !a.config.ManagementService.Config.HasCredentials() {
		a.config.ManagementService.Config.AuthInfo.ClientCertificate = filepath.Join(a.config.DataDir, DefaultCertsDirName, GeneratedCertFile)
		a.config.ManagementService.Config.AuthInfo.ClientKey = filepath.Join(a.config.DataDir, DefaultCertsDirName, KeyFile)
	}
	publicKey, privateKey, _, err := fcrypto.EnsureKey(deviceReader.PathFor(a.config.ManagementService.AuthInfo.ClientKey))
	if err != nil {
		return err
	}

	// create enrollment client
	enrollmentClient, err := newEnrollmentClient(a.config)
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

	resourceManager := resource.NewManager(
		a.log,
	)

	// create status manager
	statusManager := status.NewManager(
		deviceName,
		resourceManager,
		executer,
		a.log,
	)

	// TODO: this needs tuned
	backoff := wait.Backoff{
		Cap:      3 * time.Minute,
		Duration: 10 * time.Second,
		Factor:   1.5,
		Steps:    24,
	}

	bootstrap := device.NewBootstrap(
		deviceName,
		executer,
		deviceWriter,
		deviceReader,
		csr,
		statusManager,
		enrollmentClient,
		a.config.EnrollmentService.EnrollmentUIEndpoint,
		&a.config.ManagementService.Config,
		backoff,
		currentSpecFilePath,
		desiredSpecFilePath,
		a.log,
	)

	// bootstrap
	if err := bootstrap.Initialize(ctx); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// create the management client
	managementClient, err := newManagementClient(a.config)
	if err != nil {
		return err
	}

	// create the gRPC client
	grpcClient, err := newGrpcClient(a.config)
	if err != nil {
		a.log.Warnf("Failed to create gRPC client: %v", err)
	}

	statusManager.SetClient(managementClient)

	// create spec manager
	specManager := spec.NewManager(
		deviceName,
		currentSpecFilePath,
		desiredSpecFilePath,
		deviceWriter,
		deviceReader,
		managementClient,
		backoff,
		a.log,
	)

	// create resource controller
	resourceController := resource.NewController(
		a.log,
		resourceManager,
	)

	// create config controller
	configController := config.NewController(
		deviceWriter,
		a.log,
	)

	// create os image controller
	osImageController := device.NewOSImageController(
		executer,
		statusManager,
		a.log,
	)

	// create console controller
	consoleController := device.NewConsoleController(
		grpcClient,
		deviceName,
		a.log,
	)

	// create agent
	agent := device.NewAgent(
		deviceName,
		deviceWriter,
		statusManager,
		specManager,
		a.config.SpecFetchInterval,
		a.config.StatusUpdateInterval,
		configController,
		osImageController,
		resourceController,
		consoleController,
		a.log,
	)

	go resourceManager.Run(ctx)

	return agent.Run(ctx)
}

func newEnrollmentClient(cfg *Config) (client.Enrollment, error) {
	httpClient, err := client.NewFromConfig(&cfg.EnrollmentService.Config)
	if err != nil {
		return nil, err
	}
	return client.NewEnrollment(httpClient), nil
}

func newManagementClient(cfg *Config) (client.Management, error) {
	httpClient, err := client.NewFromConfig(&cfg.ManagementService.Config)
	if err != nil {
		return nil, err
	}
	return client.NewManagement(httpClient), nil
}

func newGrpcClient(cfg *Config) (grpc_v1.RouterServiceClient, error) {
	if cfg.GrpcManagementEndpoint == "" {
		return nil, fmt.Errorf("no gRPC endpoint, disabling console functionality")
	}
	client, err := client.NewGRPCClientFromConfig(&cfg.ManagementService.Config, cfg.GrpcManagementEndpoint)
	if err != nil {
		return nil, fmt.Errorf("creating gRPC client: %w", err)
	}
	return client, nil
}

func initializeFileIO(cfg *Config) (*fileio.Writer, *fileio.Reader) {
	deviceWriter := fileio.NewWriter()
	deviceReader := fileio.NewReader()
	testRootDir := cfg.GetTestRootDir()
	if testRootDir != "" {
		deviceWriter.SetRootdir(testRootDir)
		deviceReader.SetRootdir(testRootDir)
	}
	return deviceWriter, deviceReader
}
