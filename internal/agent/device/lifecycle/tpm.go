package lifecycle

import (
	"bytes"
	"context"
	"crypto"
	"fmt"
	"strconv"
	"strings"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm-tools/client"
)

type TPMClientConfig struct {
	Log                 *log.PrefixLogger
	DeviceWriter        fileio.ReadWriter
	PersistenceType     string
	PersistenceMetadata string
	DevicePath          string
}

type TpmClient struct {
	// path where the tpm will be found, if it exists
	tpmSysPath string
	rw         fileio.ReadWriter
	// handle to the open TPM
	tpm *tpm.TPM
	// local attestation public key
	lak *client.Key
	// meant to be updated for each attestation
	currNonce []byte
	log       *log.PrefixLogger
}

func NewTPMClient(config TPMClientConfig) (*TpmClient, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	ensureOption, err := createPersistenceOption(config)
	if err != nil {
		return nil, fmt.Errorf("creating persistence option: %w", err)
	}

	tc := &TpmClient{
		log: config.Log,
		rw:  config.DeviceWriter,
	}

	if err := tc.OpenTPM(); err != nil {
		return nil, fmt.Errorf("opening TPM: %w", err)
	}

	if err := tc.initializeKeysAndLDevID(ensureOption); err != nil {
		_ = tc.closeTPM()
		return nil, fmt.Errorf("initializing TPM keys and LDevID: %w", err)
	}

	return tc, nil
}

func validateConfig(config TPMClientConfig) error {
	if config.Log == nil {
		return fmt.Errorf("log is required")
	}
	if config.DeviceWriter == nil {
		return fmt.Errorf("device writer is required")
	}
	return nil
}

func (tc *TpmClient) initializeKeysAndLDevID(ensureOption tpm.EnsureLDevIDOption) error {
	lak, err := tc.tpm.CreateLAK()
	if err != nil {
		return fmt.Errorf("creating LAK: %w", err)
	}
	tc.lak = lak

	srk, err := tc.tpm.GenerateSRKPrimary()
	if err != nil {
		return fmt.Errorf("generating SRK: %w", err)
	}

	_, err = tc.tpm.EnsureLDevID(*srk, ensureOption)
	if err != nil {
		return fmt.Errorf("ensuring LDevID: %w", err)
	}

	return nil
}

func createPersistenceOption(config TPMClientConfig) (tpm.EnsureLDevIDOption, error) {
	if config.PersistenceType == "" {
		return nil, fmt.Errorf("persistence type is required")
	}

	switch config.PersistenceType {
	case agent_config.TPMPersistenceTypeKeyBlob:
		if config.PersistenceMetadata == "" {
			return nil, fmt.Errorf("persistence metadata is required for type %s", config.PersistenceType)
		}
		config.Log.Infof("Using key blob persisted LDevID at %s", config.PersistenceMetadata)
		return tpm.WithBlobStorage(config.PersistenceMetadata, config.DeviceWriter), nil

	case agent_config.TPMPersistenceTypeAutoHandle:
		if config.PersistenceMetadata == "" {
			return nil, fmt.Errorf("persistence metadata is required for type %s", config.PersistenceType)
		}
		config.Log.Infof("Using auto-managed handle LDevID at %s", config.PersistenceMetadata)
		return tpm.WithPersistentHandlePath(config.PersistenceMetadata, config.DeviceWriter), nil

	case agent_config.TPMPersistenceTypeFixedHandle:
		if config.PersistenceMetadata == "" {
			return nil, fmt.Errorf("persistence metadata is required for type %s", config.PersistenceType)
		}
		config.Log.Infof("Using fixed handle LDevID at %s", config.PersistenceMetadata)
		metadata := strings.TrimLeft(config.PersistenceMetadata, "0x")
		handle, err := strconv.ParseUint(metadata, 16, 32)
		if err != nil {
			return nil, fmt.Errorf("parsing handle %q: %w", config.PersistenceMetadata, err)
		}
		return tpm.WithPersistentHandle(uint32(handle)), nil

	case agent_config.TPMPersistenceTypeNone:
		config.Log.Infof("Using ephemeral (non-persistent) LDevID")
		return tpm.WithTransientKey(), nil

	default:
		return nil, fmt.Errorf("unsupported TPM LDevID persistence type %q", config.PersistenceType)
	}
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

func (tc *TpmClient) OpenTPM() error {
	var err error
	var device *tpm.Device
	if tc.tpmSysPath == "" {
		tc.log.Infof("No TPM device provided. Selecting a default device")
		device, err = tpm.ResolveDefaultDevice(tc.rw, tc.log)
	} else {
		tc.log.Infof("Using TPM device at %s", tc.tpmSysPath)
		device, err = tpm.ResolveDevice(tc.rw, tc.tpmSysPath)
	}
	if err != nil {
		return fmt.Errorf("resolving TPM device: %w", err)
	}

	tc.tpm, err = device.Open()
	if err != nil {
		return fmt.Errorf("opening TPM device: %w", err)
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
