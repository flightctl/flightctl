package lifecycle

import (
	"bytes"
	"context"
	"crypto"
	"fmt"
	"path/filepath"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm-tools/client"
)

type TPMClientConfig struct {
	Log             *log.PrefixLogger
	DeviceWriter    fileio.ReadWriter
	PersistencePath string
	DevicePath      string
	DataDir         string // Used for default persistence path construction
}

type TpmClient struct {
	// path where the tpm will be found, if it exists
	tpmSysPath string
	rw         fileio.ReadWriter
	// handle to the open TPM
	tpm *tpm.Client
	// local attestation public key
	lak *client.Key
	// meant to be updated for each attestation
	currNonce []byte
	log       *log.PrefixLogger
}

func NewTPMClient(config TPMClientConfig) (*TpmClient, error) {
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	tc := &TpmClient{
		log:        config.Log,
		rw:         config.DeviceWriter,
		tpmSysPath: config.DevicePath,
	}

	if err := tc.OpenTPM(config.PersistencePath); err != nil {
		return nil, fmt.Errorf("opening TPM: %w", err)
	}

	lak, err := tc.tpm.CreateLAK()
	if err != nil {
		_ = tc.closeTPM()
		return nil, fmt.Errorf("creating LAK: %w", err)
	}
	tc.lak = lak
	return tc, nil
}

func validateConfig(config *TPMClientConfig) error {
	if config.Log == nil {
		return fmt.Errorf("log is required")
	}
	if config.DeviceWriter == nil {
		return fmt.Errorf("device writer is required")
	}
	if config.PersistencePath == "" {
		config.PersistencePath = filepath.Join(config.DataDir, agent_config.DefaultTPMKeyBlobFile)
	}
	return nil
}

func (tc *TpmClient) GetPath() string {
	return tc.tpmSysPath
}

func (tc *TpmClient) GetLocalAttestationPubKey() crypto.PublicKey {
	return tc.lak.PublicKey()
}

func (tc *TpmClient) GetSigner() (crypto.Signer, error) {
	return tc.lak.GetSigner()
}

func (tc *TpmClient) OpenTPM(lDevIdPersistencePath string) error {
	var err error
	var device *tpm.TPM
	if tc.tpmSysPath == "" {
		tc.log.Infof("No TPM device provided. Selecting a default device")
		device, err = tpm.ResolveDefaultTPM(tc.rw, tc.log)
	} else {
		tc.log.Infof("Using TPM device at %s", tc.tpmSysPath)
		device, err = tpm.ResolveTPM(tc.rw, tc.tpmSysPath)
	}
	if err != nil {
		return fmt.Errorf("resolving TPM: %w", err)
	}

	tc.tpm, err = device.Open(lDevIdPersistencePath)
	if err != nil {
		return fmt.Errorf("opening TPM: %w", err)
	}
	return nil
}

func (tc *TpmClient) closeTPM() error {
	if tc.tpm != nil {
		err := tc.tpm.Close()
		tc.tpm = nil
		return err
	}
	return nil
}

func (tc *TpmClient) Close(ctx context.Context) error {
	if tc.lak != nil {
		tc.lak.Close()
		tc.lak = nil
	}
	return tc.closeTPM()
}

func (tc *TpmClient) TpmVendorInfoCollector(ctx context.Context) string {
	if tc == nil {
		return ""
	}
	if tc.tpm == nil {
		tc.log.Errorf("Cannot get TPM vendor info: TPM is unavailable in TpmClient")
		return ""
	}
	info, err := tc.tpm.VendorInfo()
	if err != nil {
		tc.log.Errorf("Unable to get TPM vendor info: %v", err)
		return ""
	}
	return string(info)
}

func (tc *TpmClient) TpmAttestationCollector(ctx context.Context) string {
	if tc == nil {
		return ""
	}
	if tc.tpm == nil {
		tc.log.Errorf("Cannot get TPM attestation: TPM is unavailable in TpmClient")
		return ""
	}

	att, err := tc.tpm.GetAttestation(tc.currNonce, tc.lak)
	if err != nil {
		tc.log.Errorf("Unable to get TPM attestation: %v", err)
		return ""
	}
	return att.String()
}

func (tc *TpmClient) UpdateNonce(nonce []byte) error {
	if len(nonce) < tpm.MinNonceLength {
		return fmt.Errorf("nonce does not meet minimum length of %d bytes", tpm.MinNonceLength)
	}
	if bytes.Equal(tc.currNonce, nonce) {
		return fmt.Errorf("cannot update nonce to same value as current nonce")
	}

	tc.currNonce = nonce
	return nil
}
