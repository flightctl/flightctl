package agent

import (
	"context"
	"crypto"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/action"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/client"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/tpm"
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
	publicKey, privateKey, _, err := fcrypto.EnsureKey(deviceReader.PathFor(a.config.Key))
	if err != nil {
		return err
	}

	// create enrollment client
	enrollmentClient, err := newEnrollmentClient(deviceReader, a.config)
	if err != nil {
		return err
	}

	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	if err != nil {
		return err
	}

	deviceName := hex.EncodeToString(publicKeyHash)
	csr, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), deviceName)
	if err != nil {
		return err
	}

	// initialize the TPM
	var tpmChannel *tpm.TPM
	if len(a.config.TPMPath) > 0 {
		tpmChannel, err = tpm.OpenTPM(a.config.TPMPath)
		if err != nil {
			return fmt.Errorf("opening TPM channel: %w", err)
		}
	} else {
		tpmChannel, err = tpm.OpenTPMSimulator()
		if err != nil {
			return fmt.Errorf("opening TPM simulator channel: %w", err)
		}
	}
	defer tpmChannel.Close()

	executer := &executer.CommonExecuter{}

	// create status manager
	statusManager := status.NewManager(
		deviceName,
		tpmChannel,
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
		a.config.ManagementEndpoint,
		a.config.EnrollmentUIEndpoint,
		a.config.Cacert,
		a.config.Key,
		a.config.GeneratedCert,
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
	managementClient, err := newManagementClient(deviceReader, a.config)
	if err != nil {
		return err
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

	actionManager, err := action.NewManager()
	if err != nil {
		return err
	}

	// TODO: register default actions and from config.

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
		a.log,
	)

	go agent.Run(ctx)
	go actionManager.Run(ctx)

	<-ctx.Done()
	return nil
}

func newEnrollmentClient(reader *fileio.Reader, cfg *Config) (*client.Enrollment, error) {
	httpClient, err := client.NewWithResponses(cfg.EnrollmentEndpoint,
		reader.PathFor(cfg.Cacert),
		reader.PathFor(cfg.EnrollmentCertFile),
		reader.PathFor(cfg.EnrollmentKeyFile))
	if err != nil {
		return nil, err
	}
	return client.NewEnrollment(httpClient), nil
}

func newManagementClient(reader *fileio.Reader, cfg *Config) (*client.Management, error) {
	httpClient, err := client.NewWithResponses(cfg.ManagementEndpoint, reader.PathFor(cfg.Cacert), reader.PathFor(cfg.GeneratedCert), reader.PathFor(cfg.Key))
	if err != nil {
		return nil, err
	}
	return client.NewManagement(httpClient), nil
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
