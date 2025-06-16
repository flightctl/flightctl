package tpm

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"encoding/asn1"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"os"

	"github.com/google/go-tpm-tools/client"
	pbattest "github.com/google/go-tpm-tools/proto/attest"
	pbtpm "github.com/google/go-tpm-tools/proto/tpm"
	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpmutil"
)

const (
	MinNonceLength     = 8
	TpmSystemPath      = "/dev/tpmrm0"
	TpmVersionInfoPath = "/sys/class/tpm/tpm0/tpm_version_major"
)

type TPM struct {
	devicePath string
	channel    io.ReadWriteCloser
	srk        *tpm2.NamedHandle
	ldevid     *tpm2.NamedHandle
	ldevidPub  crypto.PublicKey
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

func (t *TPM) Close() error {
	if t == nil {
		return nil
	}
	if t.channel == nil {
		return nil
	}
	return t.channel.Close()
}

func (t *TPM) GetTpmVendorInfo() ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("cannot get TPM vendor info: nil receiver in TPM struct")
	}
	if t.channel == nil {
		return nil, fmt.Errorf("cannot get TPM vendor info: no channel available in TPM struct")
	}
	return legacy.GetManufacturer(t.channel)
}

func (t *TPM) GetPCRValues(measurements map[string]string) error {
	if t == nil {
		return nil
	}
	for pcr := 1; pcr <= 16; pcr++ {
		key := fmt.Sprintf("pcr%02d", pcr)
		val, err := legacy.ReadPCR(t.channel, pcr, legacy.AlgSHA256)
		if err != nil {
			return err
		}
		measurements[key] = hex.EncodeToString(val)
	}
	return nil
}

// This function (re-)creates an ECC Primary Storage Root Key in the Owner/Storage Hierarchy.
// This key is deterministically generated from the Storage Primary Seed + input parameters.
func (t *TPM) GenerateSRKPrimary() (*tpm2.NamedHandle, error) {
	createPrimaryCmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHOwner,
		InPublic:      tpm2.New2B(tpm2.ECCSRKTemplate),
	}
	transportTPM := transport.FromReadWriter(t.channel)
	createPrimaryRsp, err := createPrimaryCmd.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("creating SRK primary: %v", err)
	}
	t.srk = &tpm2.NamedHandle{
		Handle: createPrimaryRsp.ObjectHandle,
		Name:   createPrimaryRsp.Name,
	}
	return t.srk, nil
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

// This function creates an ECC LDevID key pair under the Storage/Owner hierarchy with the Storage Root Key as parent.
func (t *TPM) CreateLDevID(srk tpm2.NamedHandle) (*tpm2.NamedHandle, error) {
	createCmd := tpm2.Create{
		ParentHandle: srk,
		InPublic:     tpm2.New2B(LDevIDTemplate),
	}
	transportTPM := transport.FromReadWriter(t.channel)
	createRsp, err := createCmd.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("executing endorsement LDevID create command: %v", err)
	}
	loadCmd := tpm2.Load{
		ParentHandle: srk,
		InPrivate:    createRsp.OutPrivate,
		InPublic:     createRsp.OutPublic,
	}

	loadRsp, err := loadCmd.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("error loading ldevid key: %v", err)
	}

	t.ldevid = &tpm2.NamedHandle{
		Handle: loadRsp.ObjectHandle,
		Name:   loadRsp.Name,
	}
	return t.ldevid, nil
}

func (t *TPM) GetLDevIDPubKey() (*crypto.PublicKey, error) {
	transportTPM := transport.FromReadWriter(t.channel)
	pub, err := tpm2.ReadPublic{
		ObjectHandle: t.ldevid.Handle,
	}.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("could not read public key: %v", err)
	}
	outpub, err := pub.OutPublic.Contents()
	if err != nil {
		return nil, fmt.Errorf("could not get contents of TPM2Bpublic: %v", err)
	}
	if outpub.Type != tpm2.TPMAlgECC {
		return nil, fmt.Errorf("public key alg %d for ldevid key is unsupported", outpub.Type)
	}
	details, err := outpub.Parameters.ECCDetail()
	if err != nil {
		return nil, fmt.Errorf("cannot read ecc details for ldevid key: %v", err)
	}
	curve, err := details.CurveID.Curve()
	if err != nil {
		return nil, fmt.Errorf("could not get curve id for ldevid key: %v", err)
	}
	unique, err := outpub.Unique.ECC()
	if err != nil {
		return nil, fmt.Errorf("could not get unique parameters for ldevid key: %v", err)
	}
	pubkey := &ecdsa.PublicKey{
		Curve: curve,
		X:     big.NewInt(0).SetBytes(unique.X.Buffer),
		Y:     big.NewInt(0).SetBytes(unique.Y.Buffer),
	}
	var cryptopubkey crypto.PublicKey = pubkey
	t.ldevidPub = cryptopubkey
	return &cryptopubkey, nil
}

func (t TPM) Public() crypto.PublicKey {
	return t.ldevidPub
}

func (t TPM) GetSigner() crypto.Signer {
	return t
}

func (t TPM) Sign(rand io.Reader, data []byte, opts crypto.SignerOpts) ([]byte, error) {
	sign := tpm2.Sign{
		KeyHandle: tpm2.NamedHandle{
			Handle: t.ldevid.Handle,
			Name:   t.ldevid.Name,
		},
		Digest: tpm2.TPM2BDigest{
			Buffer: data[:],
		},
		Validation: tpm2.TPMTTKHashCheck{
			Tag: tpm2.TPMSTHashCheck,
		},
	}

	transportTPM := transport.FromReadWriter(t.channel)
	signRsp, err := sign.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("failed to sign digest with ldevid: %v", err)
	}
	ecdsaSig, err := signRsp.Signature.Signature.ECDSA()
	if err != nil {
		return nil, fmt.Errorf("failed to get ECDSA signature from sign response: %v", err)
	}
	bigR := big.NewInt(0).SetBytes(ecdsaSig.SignatureR.Buffer)
	bigS := big.NewInt(0).SetBytes(ecdsaSig.SignatureS.Buffer)
	es := EcdsaSignature{
		R: bigR,
		S: bigS,
	}
	return asn1.Marshal(es)
}

type EcdsaSignature struct {
	R *big.Int
	S *big.Int
}

func (t *TPM) GetQuote(nonce []byte, ak *client.Key, pcr_selection *legacy.PCRSelection) (*pbtpm.Quote, error) {
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
