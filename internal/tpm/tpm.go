package tpm

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/google/go-tpm-tools/client"
	pbattest "github.com/google/go-tpm-tools/proto/attest"
	pbtpm "github.com/google/go-tpm-tools/proto/tpm"
	"github.com/google/go-tpm-tools/simulator"
	"github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpmutil"
)

const (
	MinNonceLength     = 8
	TpmSystemPath      = "/dev/tpm0"
	TpmVersionInfoPath = "/sys/class/tpm/tpm0/tpm_version_major"
)

type TPM struct {
	devicePath string
	channel    io.ReadWriteCloser
}

// Note: this may be a hardware TPM or a software or emulated TPM available to the system
func TpmExists() bool {
	if _, err := os.Stat(TpmSystemPath); err == nil {
		return true
	}
	return false
}

func ValidateTpmVersion2() error {
	if !TpmExists() {
		return fmt.Errorf("no TPM detected at %s", TpmSystemPath)
	}
	versionBytes, err := os.ReadFile(TpmVersionInfoPath)
	if err != nil {
		return err
	}
	versionStr := string(bytes.TrimSpace(versionBytes))
	if versionStr != "2" {
		return fmt.Errorf("TPM is not version 2.0")
	}
	return nil
}

func OpenTPM(devicePath string) (*TPM, error) {
	ch, err := tpmutil.OpenTPM(devicePath)
	if err != nil {
		return nil, err
	}
	return &TPM{devicePath: devicePath, channel: ch}, nil
}

func OpenTPMSimulator() (*TPM, error) {
	simulator, err := simulator.Get()
	if err != nil {
		return &TPM{}, err
	}
	return &TPM{channel: simulator}, nil
}

func (t *TPM) Close() {
	if t != nil {
		t.channel.Close()
	}
}

func (t *TPM) GetTpmVendorInfo() ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("cannot get TPM vendor info: nil receiver in TPM struct")
	}
	if t.channel == nil {
		return nil, fmt.Errorf("cannot get TPM vendor info: no channel available in TPM struct")
	}
	return tpm2.GetManufacturer(t.channel)
}

func (t *TPM) GetPCRValues(measurements map[string]string) error {
	if t == nil {
		return nil
	}
	for pcr := 1; pcr <= 16; pcr++ {
		key := fmt.Sprintf("pcr%02d", pcr)
		val, err := tpm2.ReadPCR(t.channel, pcr, tpm2.AlgSHA256)
		if err != nil {
			return err
		}
		measurements[key] = hex.EncodeToString(val)
	}
	return nil
}

// The local attestation key (LAK) is an asymmetric key that persists for the device's lifecycle (but not lifetime) and can be zeroized if needed when the device transfers ownership. (The IAK by contrast persists for the device's lifetime across uses and owners.) This key can only be used to sign TPM-internal data, ex. attestations. This is considered a Restricted signing key by the TPM.
// Key attributes:
// Restricted: yes
// Sign: yes
// Decrypt: no
// FixedTPM: yes (cannot migrate or be duplicated)
// SensitiveDataOrigin: yes (was created in the TPM)
func (t *TPM) CreateLAK() (*client.Key, error) {
	// AttestationKeyECC generates and loads a key from AKTemplateECC in the Owner (aka 'Storage') hierarchy.
	return client.AttestationKeyECC(t.channel)
}

func (t *TPM) GetAttestation(nonce []byte, ak *client.Key) (*pbattest.Attestation, error) {
	// TODO - may want to use CertChainFetcher in the AttestOpts in the future
	// see https://pkg.go.dev/github.com/google/go-tpm-tools/client#AttestOpts

	if len(nonce) < MinNonceLength {
		return nil, fmt.Errorf("nonce does not meet minimum length of %d bytes", MinNonceLength)
	}
	if ak == nil {
		return nil, fmt.Errorf("no attestation key provided")
	}

	return ak.Attest(client.AttestOpts{Nonce: nonce})
}

func (t *TPM) GetQuote(nonce []byte, ak *client.Key, pcr_selection *tpm2.PCRSelection) (*pbtpm.Quote, error) {
	if len(nonce) < MinNonceLength {
		return nil, fmt.Errorf("nonce does not meet minimum length of %d bytes", MinNonceLength)
	}

	if ak == nil {
		return nil, fmt.Errorf("no attestation key provided")
	}
	if pcr_selection == nil {
		return nil, fmt.Errorf("no pcr selection provided")
	}

	return ak.Quote(*pcr_selection, nonce)
}
