package agent

import (
	"context"
	"crypto"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/client"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/sirupsen/logrus"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

func New(log *logrus.Logger, config *Config) *Agent {
	return &Agent{
		config: config,
		log:    log,
	}
}

type Agent struct {
	config *Config
	log    *logrus.Logger
}

func (a *Agent) GetLogPrefix() string {
	return a.config.LogPrefix
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

	// ensure the agent key exists if not create it.
	publicKey, privateKey, _, err := fcrypto.EnsureKey(a.config.Key)
	if err != nil {
		return err
	}

	// create enrollment client
	enrollmentHTTPClient, err := client.NewWithResponses(a.config.EnrollmentEndpoint, a.config.Cacert, a.config.EnrollmentCertFile, a.config.EnrollmentKeyFile)
	if err != nil {
		return err
	}
	enrollmentClient := client.NewEnrollment(enrollmentHTTPClient)

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

	// create file io writer and reader
	deviceWriter, _ := initializeFileIO(a.config)

	// create config controller
	controller := config.NewController(
		enrollmentClient,
		a.config.EnrollmentEndpoint,
		a.config.EnrollmentUIEndpoint,
		a.config.ManagementEndpoint,
		a.config.Cacert,
		a.config.GeneratedCert,
		a.config.Key,
		deviceWriter,
		csr,
		a.config.LogPrefix,
	)

	// create agent
	agent := device.NewAgent(
		deviceName,
		a.config.SpecFetchInterval,
		a.config.StatusUpdateInterval,
		a.config.Cacert,
		a.config.GeneratedCert,
		a.config.Key,
		a.config.ManagementEndpoint,
		tpmChannel,
		&executer.CommonExecuter{},
		a.config.LogPrefix,
		controller,
	)

	go func() {
		if err := agent.Run(ctx); err != nil {
			a.log.Fatalf("%s: %v", a.config.LogPrefix, err)
		}
	}()

	<-ctx.Done()
	return nil
}

func initializeFileIO(cfg *Config) (*fileio.Writer, *fileio.Reader) {
	deviceWriter := fileio.NewWriter()
	deviceReader := fileio.NewReader()
	testRootDir := cfg.GetTestRootDir()
	if testRootDir != "" {
		deviceWriter.SetRootdir(testRootDir)
		deviceReader.SetRootdir(testRootDir)
	}
	return fileio.NewWriter(), fileio.NewReader()
}
