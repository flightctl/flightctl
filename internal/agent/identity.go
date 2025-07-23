package agent

import (
	"crypto"
	"fmt"
	"path/filepath"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/tpm"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/log"
)

type IdentityProvider interface {
	// EnsureIdentity ensures the identity of the device and returns the public key and private key signer
	EnsureIdentity() (crypto.PublicKey, crypto.Signer, error)
}

// newIdentityProvider creates a new identity provider based on the available hardware
func newIdentityProvider(log *log.PrefixLogger, config *agent_config.Config, rw fileio.ReadWriter) IdentityProvider {
	tpmProvider := tpmProvider{log: log, config: config}
	if tpmProvider.isAvailable() {
		log.Infof("Using TPM 2.0 for device identity")
		return &tpmProvider
	}
	log.Infof("Using file based crypto for device identity")
	return &fileBasedProvider{
		config: config,
		rw:     rw,
		log:    log,
	}
}

// tpmProvider implements identity management using TPM 2.0 LDevID
type tpmProvider struct {
	log    *log.PrefixLogger
	config *agent_config.Config
}

func (p *tpmProvider) isAvailable() bool {
	if !p.config.DisableTPM {
		return tpm.TpmExists()
	}
	return false
}

func (p *tpmProvider) EnsureIdentity() (crypto.PublicKey, crypto.Signer, error) {
	if err := tpm.ValidateTpmVersion2(); err != nil {
		return nil, nil, fmt.Errorf("TPM validation failed: %w", err)
	}

	tpm, err := tpm.OpenTPM(tpm.TpmSystemPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open TPM: %w", err)
	}

	// generate the SRK primary
	srk, err := tpm.GenerateSRKPrimary()
	if err != nil {
		tpm.Close()
		return nil, nil, fmt.Errorf("generate SRK primary: %w", err)
	}

	// create the local device identity "LDevID" key
	_, err = tpm.CreateLDevID(*srk)
	if err != nil {
		tpm.Close()
		return nil, nil, fmt.Errorf("create LDevID: %w", err)
	}

	publicKey, err := tpm.GetLDevIDPubKey()
	if err != nil {
		tpm.Close()
		return nil, nil, fmt.Errorf("get LDevID public key: %w", err)
	}

	return publicKey, tpm.GetSigner(), nil
}

// fileBasedProvider implements identity management using filesystem-stored keys
type fileBasedProvider struct {
	config *agent_config.Config
	rw     fileio.ReadWriter
	log    *log.PrefixLogger
}

func (p *fileBasedProvider) EnsureIdentity() (crypto.PublicKey, crypto.Signer, error) {
	// ensure the agent key exists if not create it.
	if !p.config.ManagementService.Config.HasCredentials() {
		p.config.ManagementService.Config.AuthInfo.ClientCertificate = filepath.Join(
			p.config.DataDir,
			agent_config.DefaultCertsDirName,
			agent_config.GeneratedCertFile,
		)
		p.config.ManagementService.Config.AuthInfo.ClientKey = filepath.Join(
			p.config.DataDir,
			agent_config.DefaultCertsDirName,
			agent_config.KeyFile,
		)
	}

	publicKey, privKey, _, err := fcrypto.EnsureKey(p.rw.PathFor(p.config.ManagementService.AuthInfo.ClientKey))
	if err != nil {
		return nil, nil, err
	}

	privateKeySigner := privKey.(crypto.Signer)
	return publicKey, privateKeySigner, nil
}
