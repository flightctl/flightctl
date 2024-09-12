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
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	gracefulShutdownTimeout = 15 * time.Second
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

	// handle teardown
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func(ctx context.Context) {
		select {
		case s := <-signals:
			a.log.Infof("Agent received shutdown signal: %s", s)
			// give the agent time to shutdown gracefully
			time.Sleep(gracefulShutdownTimeout)
			close(signals)
			cancel()
		case <-ctx.Done():
			a.log.Infof("Context has been cancelled, shutting down.")
			close(signals)
			cancel()
		}
	}(ctx)

	// create file io writer and reader
	deviceReadWriter := fileio.NewReadWriter(fileio.WithTestRootDir(a.config.testRootDir))

	// ensure the agent key exists if not create it.
	if !a.config.ManagementService.Config.HasCredentials() {
		a.config.ManagementService.Config.AuthInfo.ClientCertificate = filepath.Join(a.config.DataDir, DefaultCertsDirName, GeneratedCertFile)
		a.config.ManagementService.Config.AuthInfo.ClientKey = filepath.Join(a.config.DataDir, DefaultCertsDirName, KeyFile)
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

	// create bootc client
	bootcClient := container.NewBootcCmd(executer)

	// TODO: this needs tuned
	backoff := wait.Backoff{
		Cap:      3 * time.Minute,
		Duration: 10 * time.Second,
		Factor:   1.5,
		Steps:    24,
	}

	// create spec manager
	specManager := spec.NewManager(
		deviceName,
		a.config.DataDir,
		deviceReadWriter,
		bootcClient,
		backoff,
		a.log,
	)

	// create resource manager
	resourceManager := resource.NewManager(
		a.log,
	)

	// create hook manager
	hookManager := hook.NewManager(executer, a.log)

	// create status manager
	statusManager := status.NewManager(
		deviceName,
		resourceManager,
		hookManager,
		executer,
		a.log,
	)

	// create config controller
	configController := config.NewController(
		hookManager,
		deviceReadWriter,
		a.log,
	)

	bootstrap := device.NewBootstrap(
		deviceName,
		executer,
		deviceReadWriter,
		csr,
		specManager,
		statusManager,
		configController,
		enrollmentClient,
		a.config.EnrollmentService.EnrollmentUIEndpoint,
		&a.config.ManagementService.Config,
		backoff,
		a.log,
		a.config.DefaultLabels,
	)

	// bootstrap
	if err := bootstrap.Initialize(ctx); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// create the gRPC client this must be done after bootstrap
	grpcClient, err := newGrpcClient(a.config)
	if err != nil {
		a.log.Warnf("Failed to create gRPC client: %v", err)
	}

	// create resource controller
	resourceController := resource.NewController(
		a.log,
		resourceManager,
	)

	// create os image controller
	osImageController := device.NewOSImageController(
		executer,
		statusManager,
		specManager,
		a.log,
	)

	// create console controller
	consoleController := console.NewConsoleController(
		grpcClient,
		deviceName,
		executer,
		a.log,
	)

	// create agent
	agent := device.NewAgent(
		deviceName,
		deviceReadWriter,
		statusManager,
		specManager,
		a.config.SpecFetchInterval,
		a.config.StatusUpdateInterval,
		hookManager,
		configController,
		osImageController,
		resourceController,
		consoleController,
		a.log,
	)

	go hookManager.Run(ctx)
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
