package agent

import (
	"context"
	"crypto"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/client"
	"github.com/flightctl/flightctl/internal/agent/device"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/writer"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/sirupsen/logrus"
)

const (
	// name of the CA bundle file
	caBundleFile = "ca.crt"
	// name of the agent's key file
	agentKeyFile = "agent.key"
	// name of the enrollment certificate file
	enrollmentCertFile = "client-enrollment.crt"
	// name of the enrollment key file
	enrollmentKeyFile = "client-enrollment.key"
	// name of the management client certificate file
	clientCertFile = "client.crt"
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
	agentKeyFilePath := filepath.Join(a.config.CertDir, agentKeyFile)
	caFilePath := filepath.Join(a.config.CertDir, caBundleFile)
	enrollmentCertFilePath := filepath.Join(a.config.CertDir, enrollmentCertFile)
	enrollmentKeyFilePath := filepath.Join(a.config.CertDir, enrollmentKeyFile)
	managementCertFilePath := filepath.Join(a.config.CertDir, clientCertFile)

	// ensure the agent key exists if not create it.
	publicKey, privateKey, _, err := fcrypto.EnsureKey(agentKeyFilePath)
	if err != nil {
		return err
	}

	// create enrollment client
	enrollmentHTTPClient, err := client.NewWithResponses(a.config.EnrollmentEndpoint, caFilePath, enrollmentCertFilePath, enrollmentKeyFilePath)
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

	// create device writer
	deviceWriter := writer.New()
	if a.config.GetTestRootDir() != "" {
		a.log.Printf("Setting testRootDir is intended for testing only. Do not use in production.")
		deviceWriter.SetRootdir(a.config.GetTestRootDir())
	}

	// create config controller
	controller := config.NewController(
		enrollmentClient,
		a.config.EnrollmentEndpoint,
		a.config.ManagementEndpoint,
		caFilePath,
		managementCertFilePath,
		agentKeyFilePath,
		deviceWriter,
		csr,
		a.config.LogPrefix,
	)

	// create agent
	agent := device.NewAgent(
		deviceName,
		time.Duration(a.config.FetchSpecInterval),
		time.Duration(a.config.StatusUpdateInterval),
		time.Duration(a.config.ConfigSyncInterval),
		caFilePath,
		managementCertFilePath,
		agentKeyFilePath,
		a.config.ManagementEndpoint,
		tpmChannel,
		&executer.CommonExecuter{},
		a.config.LogPrefix,
		controller,
	)

	go func() {
		err := agent.Run(ctx)
		if err != nil {
			a.log.Fatalf("%s: %v", a.config.LogPrefix, err)
		}
	}()

	<-ctx.Done()
	return nil
}
