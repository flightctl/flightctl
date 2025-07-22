package tpm

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"encoding/asn1"
	"encoding/hex"
	"errors"
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

// Ensure TPM implements crypto.Signer interface
var _ crypto.Signer = (*TPM)(nil)

const (
	MinNonceLength     = 8
	TpmSystemPath      = "/dev/tpmrm0"
	TpmVersionInfoPath = "/sys/class/tpm/tpm0/tpm_version_major"
)

type TPM struct {
	devicePath string
	conn       io.ReadWriteCloser
	srk        *tpm2.NamedHandle
	ldevid     *tpm2.NamedHandle
	ldevidPub  crypto.PublicKey
	cleanup    func() error
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
		return fmt.Errorf("failed to read TPM version info from %s: %w", TpmVersionInfoPath, err)
	}
	versionStr := string(bytes.TrimSpace(versionBytes))
	if versionStr != "2" {
		return fmt.Errorf("TPM is not version 2.0")
	}
	return nil
}

func OpenTPM(devicePath string) (*TPM, error) {
	conn, err := tpmutil.OpenTPM(devicePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open TPM device at %s: %w", devicePath, err)
	}

	t := &TPM{
		devicePath: devicePath,
		conn:       conn,
		cleanup: func() error {
			return conn.Close()
		},
	}

	_, err = t.generateSRKPrimary()
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("generating SRK: %w", err)
	}

	_, err = t.createLDevID()
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("creating LDevID: %w", err)
	}

	_, err = t.getLDevIDPubKey()
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("reading LDevID public key: %w", err)
	}

	return t, nil
}

func (t *TPM) Close() error {
	if t == nil {
		return nil
	}
	if t.cleanup != nil {
		return t.cleanup()
	}
	return nil
}

func (t *TPM) GetTpmVendorInfo() ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("cannot get TPM vendor info: nil receiver")
	}
	if t.conn == nil {
		return nil, fmt.Errorf("cannot get TPM vendor info: no conn available")
	}
	vendorInfo, err := legacy.GetManufacturer(t.conn)
	if err != nil {
		return nil, fmt.Errorf("failed to get TPM manufacturer info: %w", err)
	}
	return vendorInfo, nil
}

func (t *TPM) GetPCRValues(measurements map[string]string) error {
	if t == nil {
		return nil
	}
	for pcr := 1; pcr <= 16; pcr++ {
		key := fmt.Sprintf("pcr%02d", pcr)
		val, err := legacy.ReadPCR(t.conn, pcr, legacy.AlgSHA256)
		if err != nil {
			return fmt.Errorf("failed to read PCR %d: %w", pcr, err)
		}
		measurements[key] = hex.EncodeToString(val)
	}
	return nil
}

// generateSRKPrimary (re-)creates an ECC Primary Storage Root Key in the Owner/Storage Hierarchy.
// This key is deterministically generated from the Storage Primary Seed + input parameters.
func (t *TPM) generateSRKPrimary() (*tpm2.NamedHandle, error) {
	createPrimaryCmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHOwner,
		InPublic:      tpm2.New2B(tpm2.ECCSRKTemplate),
	}
	createPrimaryRsp, err := createPrimaryCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("creating SRK primary: %w", err)
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
	return client.AttestationKeyECC(t.conn)
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

	attestation, err := ak.Attest(client.AttestOpts{Nonce: nonce})
	if err != nil {
		return nil, fmt.Errorf("failed to get attestation: %w", err)
	}
	return attestation, nil
}

// createLDevID creates an ECC LDevID key pair under the Storage/Owner hierarchy with the Storage Root Key as parent.
func (t *TPM) createLDevID() (*tpm2.NamedHandle, error) {
	if t.srk == nil {
		return nil, fmt.Errorf("SRK not initialized")
	}
	createCmd := tpm2.Create{
		ParentHandle: *t.srk,
		InPublic:     tpm2.New2B(LDevIDTemplate),
	}
	createRsp, err := createCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("executing endorsement LDevID create command: %w", err)
	}
	loadCmd := tpm2.Load{
		ParentHandle: *t.srk,
		InPrivate:    createRsp.OutPrivate,
		InPublic:     createRsp.OutPublic,
	}

	loadRsp, err := loadCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("error loading ldevid key: %w", err)
	}

	t.ldevid = &tpm2.NamedHandle{
		Handle: loadRsp.ObjectHandle,
		Name:   loadRsp.Name,
	}
	return t.ldevid, nil
}

func (t *TPM) getLDevIDPubKey() (crypto.PublicKey, error) {
	if t.ldevid == nil {
		return nil, fmt.Errorf("ldevid not initialized")
	}

	pub, err := tpm2.ReadPublic{
		ObjectHandle: t.ldevid.Handle,
	}.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("could not read public key: %w", err)
	}
	outpub, err := pub.OutPublic.Contents()
	if err != nil {
		return nil, fmt.Errorf("could not get contents of TPM2Bpublic: %w", err)
	}
	if outpub.Type != tpm2.TPMAlgECC {
		return nil, fmt.Errorf("public key alg %d for ldevid key is unsupported", outpub.Type)
	}
	details, err := outpub.Parameters.ECCDetail()
	if err != nil {
		return nil, fmt.Errorf("cannot read ecc details for ldevid key: %w", err)
	}
	curve, err := details.CurveID.Curve()
	if err != nil {
		return nil, fmt.Errorf("could not get curve id for ldevid key: %w", err)
	}
	unique, err := outpub.Unique.ECC()
	if err != nil {
		return nil, fmt.Errorf("could not get unique parameters for ldevid key: %w", err)
	}
	pubkey := &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(unique.X.Buffer),
		Y:     new(big.Int).SetBytes(unique.Y.Buffer),
	}
	// converts ecdsa.PublicKey to crypto.PublicKey
	t.ldevidPub = pubkey
	return pubkey, nil
}

func (t *TPM) Public() crypto.PublicKey {
	return t.ldevidPub
}

func (t *TPM) GetSigner() crypto.Signer {
	return t
}

// Sign signs the given data using the TPM's LDevID key.
// The rand parameter is ignored as the TPM generates its own randomness internally.
// Opts is ignored as the only hash type supported is SHA256 (as defined by the creation of the key)
func (t *TPM) Sign(rand io.Reader, data []byte, opts crypto.SignerOpts) ([]byte, error) {
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

	signRsp, err := sign.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("failed to sign digest with ldevid: %w", err)
	}
	ecdsaSig, err := signRsp.Signature.Signature.ECDSA()
	if err != nil {
		return nil, fmt.Errorf("failed to get ECDSA signature from sign response: %w", err)
	}
	bigR := new(big.Int).SetBytes(ecdsaSig.SignatureR.Buffer)
	bigS := new(big.Int).SetBytes(ecdsaSig.SignatureS.Buffer)
	es := ecdsaSignature{
		R: bigR,
		S: bigS,
	}
	signature, err := asn1.Marshal(es)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ECDSA signature: %w", err)
	}
	return signature, nil
}

type ecdsaSignature struct {
	R *big.Int
	S *big.Int
}

func (t *TPM) endorsementKeyCert() (*client.Key, error) {
	if t.conn == nil {
		return nil, fmt.Errorf("cannot read endorsement key certificate: no connection available")
	}
	// gather errors so that we can report all the types we attempted
	// but if any method returns a key we return that key and drop the errors
	var errs []error
	key, err := client.EndorsementKeyRSA(t.conn)
	if err != nil {
		errs = append(errs, fmt.Errorf("reading rsa endorsement %w", err))
	} else {
		return key, nil
	}

	key, err = client.EndorsementKeyECC(t.conn)
	if err != nil {
		errs = append(errs, fmt.Errorf("reading ecc endorsement %w", err))
	} else {
		return key, nil
	}
	return nil, errors.Join(errs...)
}

func (t *TPM) EndorsementKeyCert() ([]byte, error) {
	key, err := t.endorsementKeyCert()
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	return key.CertDERBytes(), nil
}

func (t *TPM) EndorsementKeyPublic() ([]byte, error) {
	key, err := t.endorsementKeyCert()
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	res, err := key.PublicArea().Encode()
	if err != nil {
		return nil, fmt.Errorf("encoding public key: %w", err)
	}
	return res, nil
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

	quote, err := ak.Quote(*pcr_selection, nonce)
	if err != nil {
		return nil, fmt.Errorf("failed to get TPM quote: %w", err)
	}
	return quote, nil
}
