package tpm

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm-tools/client"
	pbattest "github.com/google/go-tpm-tools/proto/attest"
	pbtpm "github.com/google/go-tpm-tools/proto/tpm"
	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpmutil"
	"sigs.k8s.io/yaml"
)

// Ensure Client implements crypto.Signer interface
var _ crypto.Signer = (*Client)(nil)

// ldevIDBlob represents a serialized LDevID key pair for storage.
type ldevIDBlob struct {
	// PublicBlob contains the serialized public key data.
	PublicBlob []byte `json:"public"`
	// PrivateBlob contains the serialized private key data.
	PrivateBlob []byte `json:"private"`
}

// ClientConfig contains configuration options for creating a TPM client.
type ClientConfig struct {
	Log             *log.PrefixLogger
	DeviceWriter    fileio.ReadWriter
	PersistencePath string
	DevicePath      string
	DataDir         string // Used for default persistence path construction
}

// Client represents a connection to a TPM device and manages TPM operations.
type Client struct {
	rmPath    string
	sysPath   string
	conn      io.ReadWriteCloser
	rw        fileio.ReadWriter
	srk       *tpm2.NamedHandle
	ldevid    *tpm2.NamedHandle
	ldevidPub crypto.PublicKey
	lak       *client.Key
	currNonce []byte
	log       *log.PrefixLogger
}

// NewClient creates a new TPM client with the given configuration.
func NewClient(log *log.PrefixLogger, rw fileio.ReadWriter, config *agent_config.Config) (*Client, error) {
	sysPath := config.TPM.Path
	tpm, err := resolveFromPath(rw, log, sysPath)
	if err != nil {
		return nil, fmt.Errorf("resolving TPM: %w", err)
	}

	// open the TPM connection
	conn, err := tpmutil.OpenTPM(tpm.resourceMgrPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open TPM device at %s: %w", tpm.resourceMgrPath, err)
	}

	client := &Client{
		rmPath:  tpm.resourceMgrPath,
		rw:      rw,
		conn:    conn,
		sysPath: sysPath,
		log:     log,
	}

	ctx := context.Background()
	_, err = client.generateSRKPrimary()
	if err != nil {
		_ = client.Close(ctx)
		return nil, fmt.Errorf("generating SRK: %w", err)
	}

	_, err = client.ensureLDevID(config.PersistencePath)
	if err != nil {
		_ = client.Close(ctx)
		return nil, fmt.Errorf("creating LDevID: %w", err)
	}

	_, err = client.getLDevIDPubKey()
	if err != nil {
		_ = client.Close(ctx)
		return nil, fmt.Errorf("reading LDevID public key: %w", err)
	}

	// Create Local Attestation Key
	lak, err := client.CreateLAK()
	if err != nil {
		_ = client.Close(ctx)
		return nil, fmt.Errorf("creating LAK: %w", err)
	}
	client.lak = lak

	return client, nil
}

// GetPath returns the TPM device path.
func (t *Client) GetPath() string {
	return t.sysPath
}

// GetLocalAttestationPubKey returns the public key of the Local Attestation Key.
func (t *Client) GetLocalAttestationPubKey() crypto.PublicKey {
	if t.lak == nil {
		return nil
	}
	return t.lak.PublicKey()
}

// UpdateNonce updates the current nonce for attestation operations.
func (t *Client) UpdateNonce(nonce []byte) error {
	if len(nonce) < MinNonceLength {
		return fmt.Errorf("nonce does not meet minimum length of %d bytes", MinNonceLength)
	}
	if bytes.Equal(t.currNonce, nonce) {
		return fmt.Errorf("cannot update nonce to same value as current nonce")
	}

	t.currNonce = nonce
	return nil
}

// VendorInfoCollector returns TPM vendor information as a string for system info collection.
func (t *Client) VendorInfoCollector(ctx context.Context) string {
	if t == nil {
		return ""
	}
	if t.conn == nil {
		if t.log != nil {
			t.log.Errorf("Cannot get TPM vendor info: TPM connection is unavailable")
		}
		return ""
	}
	info, err := t.VendorInfo()
	if err != nil {
		if t.log != nil {
			t.log.Errorf("Unable to get TPM vendor info: %v", err)
		}
		return ""
	}
	return string(info)
}

// AttestationCollector returns TPM attestation as a string for system info collection.
func (t *Client) AttestationCollector(ctx context.Context) string {
	if t == nil {
		return ""
	}
	if t.conn == nil {
		if t.log != nil {
			t.log.Errorf("Cannot get TPM attestation: TPM connection is unavailable")
		}
		return ""
	}
	if t.lak == nil {
		if t.log != nil {
			t.log.Errorf("Cannot get TPM attestation: LAK is not available")
		}
		return ""
	}

	att, err := t.GetAttestation(t.currNonce, t.lak)
	if err != nil {
		if t.log != nil {
			t.log.Errorf("Unable to get TPM attestation: %v", err)
		}
		return ""
	}
	return att.String()
}

// Close closes the TPM connection and flushes any transient handles.
// It should be called when the TPM is no longer needed to free resources.
func (t *Client) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	var errs []error

	// Close LAK if it exists
	if t.lak != nil {
		t.lak.Close()
		t.lak = nil
	}

	// Flush transient handles before closing
	if t.srk != nil {
		if err := t.flushContextForHandle(t.srk.Handle); err != nil {
			errs = append(errs, fmt.Errorf("flushing SRK handle: %w", err))
		}
		t.srk = nil
	}

	if t.ldevid != nil {
		if err := t.flushContextForHandle(t.ldevid.Handle); err != nil {
			errs = append(errs, fmt.Errorf("flushing LDevID handle: %w", err))
		}
		t.ldevid = nil
	}

	if t.conn != nil {
		if err := t.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing TPM channel: %w", err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// VendorInfo returns the TPM manufacturer information.
// This can be used to identify the TPM vendor and model.
func (t *Client) VendorInfo() ([]byte, error) {
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

// ReadPCRValues reads PCR values from the TPM and populates the provided map.
// The map keys are formatted as "pcr01", "pcr02", etc., and values are hex-encoded.
func (t *Client) ReadPCRValues(measurements map[string]string) error {
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
func (t *Client) generateSRKPrimary() (*tpm2.NamedHandle, error) {
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

// CreateLAK creates a Local Attestation Key (LAK) for TPM attestation operations.
// The LAK is an asymmetric key that persists for the device's lifecycle and can be used
// to sign TPM-internal data such as attestations. This is a Restricted signing key.
// Key attributes: Restricted=yes, Sign=yes, Decrypt=no, FixedTPM=yes, SensitiveDataOrigin=yes
func (t *Client) CreateLAK() (*client.Key, error) {
	// AttestationKeyECC generates and loads a key from AKTemplateECC in the Owner (aka 'Storage') hierarchy.
	return client.AttestationKeyECC(t.conn)
}

// GetAttestation generates a TPM attestation using the provided nonce and attestation key.
// The nonce must be at least MinNonceLength bytes long for security.
func (t *Client) GetAttestation(nonce []byte, ak *client.Key) (*pbattest.Attestation, error) {
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
func (t *Client) createLDevID() (*tpm2.NamedHandle, error) {
	if t.srk == nil {
		return nil, fmt.Errorf("SRK not initialized")
	}
	createCmd := tpm2.Create{
		ParentHandle: *t.srk,
		InPublic:     tpm2.New2B(LDevIDTemplate),
	}
	createRsp, err := createCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("executing LDevID create command: %w", err)
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

func (t *Client) getLDevIDPubKey() (crypto.PublicKey, error) {
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

func (t *Client) Public() crypto.PublicKey {
	return t.ldevidPub
}

func (t *Client) GetSigner() crypto.Signer {
	return t
}

// Sign signs the given data using the TPM's LDevID key.
// The rand parameter is ignored as the TPM generates its own randomness internally.
// Opts is ignored as the only hash type supported is SHA256 (as defined by the creation of the key)
func (t *Client) Sign(rand io.Reader, data []byte, opts crypto.SignerOpts) ([]byte, error) {
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

func (t *Client) endorsementKeyCert() (*client.Key, error) {
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

func (t *Client) EndorsementKeyCert() ([]byte, error) {
	key, err := t.endorsementKeyCert()
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	return key.CertDERBytes(), nil
}

func (t *Client) EndorsementKeyPublic() ([]byte, error) {
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

// loadLDevIDFromBlob will load a LDevID for the existing SRK from key blob parts
// According to https://trustedcomputinggroup.org/wp-content/uploads/TPM-2p0-Keys-for-Device-Identity-and-Attestation_v1_r12_pub10082021.pdf
// the blobs returned are safe to be stored as the private portion returned is encrypted by the TPM.
func (t *Client) loadLDevIDFromBlob(public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) (*tpm2.NamedHandle, error) {
	loadCmd := tpm2.Load{
		ParentHandle: t.srk,
		InPrivate:    private,
		InPublic:     public,
	}

	loadRsp, err := loadCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("loading ldevid key: %w", err)
	}

	t.ldevid = &tpm2.NamedHandle{
		Handle: loadRsp.ObjectHandle,
		Name:   loadRsp.Name,
	}
	return t.ldevid, nil
}

// flushContextForHandle flushes the TPM context for the specified handle if it's transient.
// Persistent handles are not flushed as they remain in the TPM across reboots.
func (t *Client) flushContextForHandle(handle tpm2.TPMHandle) error {
	// Only flush if this is a transient handle (not a persistent handle)
	if handle < persistentHandleMin || handle > persistentHandleMax {
		flushCmd := tpm2.FlushContext{
			FlushHandle: handle,
		}

		_, err := flushCmd.Execute(transport.FromReadWriter(t.conn))
		if err != nil {
			return fmt.Errorf("flushing context for handle 0x%x: %w", handle, err)
		}
	}
	return nil
}

// GetQuote generates a TPM quote using the provided nonce, attestation key, and PCR selection.
// The quote provides cryptographic evidence of the current PCR values.
func (t *Client) GetQuote(nonce []byte, ak *client.Key, pcr_selection *legacy.PCRSelection) (*pbtpm.Quote, error) {
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

func (t *Client) saveLDevIDBlob(public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate, path string) error {
	blob := ldevIDBlob{
		PublicBlob:  public.Bytes(),
		PrivateBlob: private.Buffer,
	}

	data, err := yaml.Marshal(blob)
	if err != nil {
		return fmt.Errorf("marshaling blob to YAML: %w", err)
	}

	err = t.rw.WriteFile(path, data, 0600)
	if err != nil {
		return fmt.Errorf("writing blob to file %s: %v", path, err)
	}

	return nil
}

func (t *Client) loadLDevIDBlob(path string) (*tpm2.TPM2BPublic, *tpm2.TPM2BPrivate, error) {
	var public tpm2.TPM2BPublic
	var private tpm2.TPM2BPrivate

	data, err := t.rw.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	var blob ldevIDBlob
	err = yaml.Unmarshal(data, &blob)
	if err != nil {
		return nil, nil, fmt.Errorf("unmarshaling YAML from file %s: %v", path, err)
	}

	public = tpm2.BytesAs2B[tpm2.TPMTPublic](blob.PublicBlob)
	private.Buffer = blob.PrivateBlob

	return &public, &private, nil
}

// ensureLDevID ensures an LDevID key exists using blob storage at the specified path.
// The Storage Root Key (srk) is used as the parent for the LDevID.
func (t *Client) ensureLDevID(path string) (*tpm2.NamedHandle, error) {
	if path == "" {
		return nil, fmt.Errorf("blob path cannot be empty")
	}

	// Try to load existing blob from file
	public, private, err := t.loadLDevIDBlob(path)
	if err == nil {
		return t.loadLDevIDFromBlob(*public, *private)
	}

	// If file doesn't exist, create new key and persist it
	if os.IsNotExist(err) {
		createCmd := tpm2.Create{
			ParentHandle: *t.srk,
			InPublic:     tpm2.New2B(LDevIDTemplate),
		}
		transportTPM := transport.FromReadWriter(t.conn)
		createRsp, err := createCmd.Execute(transportTPM)
		if err != nil {
			return nil, fmt.Errorf("creating LDevID key: %w", err)
		}

		err = t.saveLDevIDBlob(createRsp.OutPublic, createRsp.OutPrivate, path)
		if err != nil {
			return nil, fmt.Errorf("saving blob to file: %w", err)
		}

		return t.loadLDevIDFromBlob(createRsp.OutPublic, createRsp.OutPrivate)
	}

	// File exists but couldn't be loaded (corrupted, invalid format, etc.)
	return nil, fmt.Errorf("loading blob from file %s: %w", path, err)
}
