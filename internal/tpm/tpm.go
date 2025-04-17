package tpm

import (
	"encoding/hex"
	"fmt"
	"io"

	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm-tools/simulator"
	pbtpm "github.com/google/go-tpm-tools/proto/tpm"
	pbattest "github.com/google/go-tpm-tools/proto/attest"
	"github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpmutil"
)

type TPM struct {
	devicePath string
	channel    io.ReadWriteCloser
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
	// AttestationKeyECC generates and loads a key from AKTemplateECC in the Owner hierarchy.
	// TODO - shouldn't this be in the Storage hierarchy because it's a local key?
	lak, err := client.AttestationKeyECC(t.channel)
	if err != nil {
		return nil, err
	}
	return lak, nil
}

// The local device ID (LDevID) is an asymmetric key that persists for the device's lifecycle (but not lifetime) and can be zeroized if needed when the device transfers ownership. (The IDevID by contrast persists for the device's lifetime across uses and owners.) This key can be used for general device authentication, ex. TLS. This is considered a non-Restricted signing key by the TPM.
// Key attributes:
// Restricted: no
// Sign: yes
// Decrypt: no
// FixedTPM: yes (cannot migrate or be duplicated)
// SensitiveDataOrigin: yes (was created in the TPM)
func (t *TPM) CreateLDevID() (*client.Key, error) {
	return nil, fmt.Errorf("todo")

	// handle is supposed to be a uint32 but it's unclear in tpmutil how you get this value
	// 'type Handle is a reference to a TPM object'
	//h := handle.HandleValue()

	// type Public frpm tpm2 looks like this:
	/* type Public struct {
	Type       Algorithm
	NameAlg    Algorithm
	Attributes KeyProp
	AuthPolicy tpmutil.U16Bytes

	// RSAParameters contains both [rsa]parameters and [rsa]unique.
	RSAParameters *RSAParams
	// ECCParameters contains both [ecc]parameters and [ecc]unique.
	ECCParameters *ECCParams
	// SymCipherParameters contains both [sym]parameters and [sym]unique.
	SymCipherParameters *SymCipherParams
	// KeyedHashParameters contains both [keyedHash]parameters and [keyedHash]unique.
	KeyedHashParameters *KeyedHashParams
	}*/

	//ldevid, err := client.NewKey(t.channel, parent tpmutil.Handle, template tpm2.Public)
}

func (t *TPM) GetAttestation(nonce []byte, ak *client.Key) (*pbattest.Attestation, error) {
	// todo - may want to use CertChainFetcher in the AttestOpts in the future
	// see https://pkg.go.dev/github.com/google/go-tpm-tools/client#AttestOpts

	att, err := ak.Attest(client.AttestOpts{Nonce: nonce})
	if err != nil {
		return nil, err
	}
	return att, nil
}

//todo - make sure nonce is >= 8 bytes
func (t *TPM) GetQuote(nonce []byte, ak *client.Key, pcr_selection *tpm2.PCRSelection) (*pbtpm.Quote, error) {
	quote, err := ak.Quote(*pcr_selection, nonce)
        if err != nil {
                return nil, err
        }
	return quote, nil
}
