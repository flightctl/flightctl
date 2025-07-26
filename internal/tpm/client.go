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
	"io/fs"
	"math/big"

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
)

var (
	// errHandleBlobNotFound indicates that no LDevID data was found in the TPM blob
	errHandleBlobNotFound = errors.New("handle blob not found")
)

// Ensure Client implements crypto.Signer interface
var _ crypto.Signer = (*Client)(nil)

// ClientConfig contains configuration options for creating a TPM client.
type ClientConfig struct {
	Log             *log.PrefixLogger
	DeviceWriter    fileio.ReadWriter
	PersistencePath string
	DevicePath      string
}

// Client represents a connection to a TPM device and manages TPM operations.
type Client struct {
	sysPath              string
	conn                 io.ReadWriteCloser
	rw                   fileio.ReadWriter
	srk                  *tpm2.NamedHandle
	ldevid               *tpm2.NamedHandle
	ldevidPub            crypto.PublicKey
	lak                  *tpm2.NamedHandle
	currNonce            []byte
	storageHierarchyAuth []byte
	log                  *log.PrefixLogger
	persistence          *persistence
	ownership            *ownership
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

	return newClientWithConnection(conn, sysPath, log, rw, config)
}

// newClientWithConnection creates a new TPM client with the provided connection.
// This helper function is useful for testing with simulators.
func newClientWithConnection(conn io.ReadWriteCloser, sysPath string, log *log.PrefixLogger, rw fileio.ReadWriter, config *agent_config.Config) (*Client, error) {
	client := &Client{
		rw:      rw,
		conn:    conn,
		sysPath: sysPath,
		log:     log,
	}

	// Create persistence and ownership components
	var err error
	client.persistence, err = newPersistence(rw, config.TPM.PersistencePath)
	if err != nil {
		return nil, fmt.Errorf("creating persistence: %w", err)
	}
	client.ownership = newOwnership(client, client.persistence)

	ctx := context.Background()

	if config.TPM.EnableOwnership {
		password, err := client.ownership.ensureStorageHierarchyPassword()
		if err != nil {
			_ = client.Close(ctx)
			return nil, fmt.Errorf("ensuring storage hierarchy password: %w", err)
		}
		client.storageHierarchyAuth = password
	} else {
		client.storageHierarchyAuth = nil
	}

	_, err = client.generateSRKPrimary()
	if err != nil {
		_ = client.Close(ctx)
		return nil, fmt.Errorf("generating SRK: %w", err)
	}

	_, err = client.ensureLDevID()
	if err != nil {
		_ = client.Close(ctx)
		return nil, fmt.Errorf("creating LDevID: %w", err)
	}

	_, err = client.getLDevIDPubKey()
	if err != nil {
		_ = client.Close(ctx)
		return nil, fmt.Errorf("reading LDevID public key: %w", err)
	}

	_, err = client.ensureLAK()
	if err != nil {
		_ = client.Close(ctx)
		return nil, fmt.Errorf("creating LAK: %w", err)
	}

	return client, nil
}

// GetPath returns the TPM device path.
func (t *Client) GetPath() string {
	return t.sysPath
}

// GetLocalAttestationPubKey returns the public key of the Local Attestation Key.
func (t *Client) GetLocalAttestationPubKey() (crypto.PublicKey, error) {
	if t.lak == nil {
		return nil, fmt.Errorf("lak is not yet initialized")
	}
	return t.eccPublicKey(t.lak)
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
		if err := t.flushContextForHandle(t.lak.Handle); err != nil {
			errs = append(errs, fmt.Errorf("flushing LAK handle: %w", err))
		}
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
		PrimaryHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHOwner,
			Auth:   tpm2.PasswordAuth(t.storageHierarchyAuth),
		},
		InPublic: tpm2.New2B(tpm2.ECCSRKTemplate),
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
// The LAK is created as a child of the SRK to properly handle storage hierarchy authentication.
func (t *Client) ensureLAK() (*tpm2.NamedHandle, error) {
	var err error
	t.lak, err = t.ensureKey(t.persistence.loadLAKBlob, t.persistence.saveLAKBlob, AttestationKeyTemplate)
	if err != nil {
		return nil, fmt.Errorf("ensuring LAK: %w", err)
	}
	return t.lak, nil
}

// GetAttestation generates a TPM attestation using the provided nonce and attestation key.
// The nonce must be at least MinNonceLength bytes long for security.
func (t *Client) GetAttestation(nonce []byte, ak *tpm2.NamedHandle) (*pbattest.Attestation, error) {
	// TODO - may want to use CertChainFetcher in the AttestOpts in the future
	// see https://pkg.go.dev/github.com/google/go-tpm-tools/client#AttestOpts

	if len(nonce) < MinNonceLength {
		return nil, fmt.Errorf("nonce does not meet minimum length of %d bytes", MinNonceLength)
	}
	if ak == nil {
		return nil, fmt.Errorf("no attestation key provided")
	}

	akPubKey, err := t.getAttestationKeyPublic(ak)
	if err != nil {
		return nil, fmt.Errorf("failed to get AK public key: %w", err)
	}

	pcrSelection := createFullPCRSelection()

	quote, err := t.GetQuote(nonce, ak, pcrSelection)
	if err != nil {
		return nil, fmt.Errorf("failed to get quote: %w", err)
	}

	// Create attestation response
	attestation := &pbattest.Attestation{
		AkPub:  akPubKey,
		Quotes: []*pbtpm.Quote{quote},
		// Other fields like AkCert, IntermediateCerts are optional
	}

	return attestation, nil
}

// getAttestationKeyPublic reads the public area of the attestation key and marshals it
func (t *Client) getAttestationKeyPublic(ak *tpm2.NamedHandle) ([]byte, error) {
	readPubCmd := tpm2.ReadPublic{
		ObjectHandle: ak.Handle,
	}

	readPubRsp, err := readPubCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("failed to read public area: %w", err)
	}

	// Marshal the public area to bytes
	pubBytes := tpm2.Marshal(readPubRsp.OutPublic)
	return pubBytes, nil
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
func (t *Client) eccPublicKey(namedHandle *tpm2.NamedHandle) (crypto.PublicKey, error) {
	if namedHandle == nil {
		return nil, fmt.Errorf("invalid handle provided")
	}
	pub, err := tpm2.ReadPublic{
		ObjectHandle: namedHandle.Handle,
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
	return pubkey, nil
}

func (t *Client) getLDevIDPubKey() (crypto.PublicKey, error) {
	pubKey, err := t.eccPublicKey(t.ldevid)
	if err != nil {
		return nil, fmt.Errorf("ldevid public key: %w", err)
	}
	t.ldevidPub = pubKey
	return pubKey, nil
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

func (t *Client) endorsementKey() (*client.Key, error) {
	if t.conn == nil {
		return nil, fmt.Errorf("cannot read endorsement key certificate: no connection available")
	}
	// gather errors so that we can report all the types we attempted
	// but if any method returns a key we return that key and drop the errors
	var errs []error
	keyFactories := []struct {
		name    string
		factory func(io.ReadWriter) (*client.Key, error)
	}{
		{"rsa", client.EndorsementKeyRSA},
		{"ecc", client.EndorsementKeyECC},
	}
	for _, keyFactory := range keyFactories {
		key, err := keyFactory.factory(t.conn)
		if err == nil {
			return key, nil
		}
		errs = append(errs, fmt.Errorf("reading %s endorsement: %w", keyFactory.name, err))
	}
	return nil, errors.Join(errs...)
}

func (t *Client) EndorsementKeyCert() ([]byte, error) {
	key, err := t.endorsementKey()
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	defer key.Close()
	return key.CertDERBytes(), nil
}

func (t *Client) EndorsementKeyPublic() ([]byte, error) {
	key, err := t.endorsementKey()
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	res, err := key.PublicArea().Encode()
	if err != nil {
		return nil, fmt.Errorf("encoding public key: %w", err)
	}
	defer key.Close()
	return res, nil
}

// loadKeyFromBlob will load a key for the existing SRK from key blob parts
// According to https://trustedcomputinggroup.org/wp-content/uploads/TPM-2p0-Keys-for-Device-Identity-and-Attestation_v1_r12_pub10082021.pdf
// the blobs returned are safe to be stored as the private portion returned is encrypted by the TPM.
func (t *Client) loadKeyFromBlob(public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) (*tpm2.NamedHandle, error) {
	loadCmd := tpm2.Load{
		ParentHandle: t.srk,
		InPrivate:    private,
		InPublic:     public,
	}

	loadRsp, err := loadCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("loading key: %w", err)
	}

	return &tpm2.NamedHandle{
		Handle: loadRsp.ObjectHandle,
		Name:   loadRsp.Name,
	}, nil
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
func (t *Client) GetQuote(nonce []byte, ak *tpm2.NamedHandle, pcrSelection *tpm2.TPMLPCRSelection) (*pbtpm.Quote, error) {
	if len(nonce) < MinNonceLength {
		return nil, fmt.Errorf("nonce does not meet minimum length of %d bytes", MinNonceLength)
	}
	if ak == nil {
		return nil, fmt.Errorf("no attestation key provided")
	}
	if pcrSelection == nil {
		return nil, fmt.Errorf("no pcr selection provided")
	}

	// Create TPM2 Quote command using the correct API
	quoteCmd := tpm2.Quote{
		SignHandle: tpm2.AuthHandle{
			Handle: ak.Handle,
			Name:   ak.Name,
			Auth:   tpm2.PasswordAuth(nil), // LAK uses password auth
		},
		QualifyingData: tpm2.TPM2BData{Buffer: nonce},
		InScheme: tpm2.TPMTSigScheme{
			Scheme: tpm2.TPMAlgECDSA,
			Details: tpm2.NewTPMUSigScheme(
				tpm2.TPMAlgECDSA,
				&tpm2.TPMSSchemeHash{HashAlg: tpm2.TPMAlgSHA256},
			),
		},
		PCRSelect: *pcrSelection,
	}

	quoteRsp, err := quoteCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("failed to execute TPM quote: %w", err)
	}

	// Convert signature to bytes using Marshal
	sigBytes := tpm2.Marshal(quoteRsp.Signature)

	// Create the quote response in the expected protobuf format
	quote := &pbtpm.Quote{
		Quote:  quoteRsp.Quoted.Bytes(),
		RawSig: sigBytes,
	}

	pcrs, err := client.ReadPCRs(t.conn, convertTPMLPCRSelectionToPCRSelection(pcrSelection))
	if err != nil {
		return nil, fmt.Errorf("reading PCRs: %w", err)
	}

	quote.Pcrs = pcrs

	return quote, nil
}

func (t *Client) ensureKey(load loadBlobFunc, save saveBlobFunc, template tpm2.TPMTPublic) (*tpm2.NamedHandle, error) {
	// Try to load existing blob from file
	public, private, err := load()
	if err == nil {
		return t.loadKeyFromBlob(*public, *private)
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errHandleBlobNotFound) {
		createCmd := tpm2.Create{
			ParentHandle: *t.srk,
			InPublic:     tpm2.New2B(template),
		}
		transportTPM := transport.FromReadWriter(t.conn)
		createRsp, err := createCmd.Execute(transportTPM)
		if err != nil {
			return nil, fmt.Errorf("creating LDevID key: %w", err)
		}

		err = save(createRsp.OutPublic, createRsp.OutPrivate)
		if err != nil {
			return nil, fmt.Errorf("saving blob to file: %w", err)
		}

		return t.loadKeyFromBlob(createRsp.OutPublic, createRsp.OutPrivate)
	}
	// File exists but couldn't be loaded (corrupted, invalid format, etc.)
	return nil, fmt.Errorf("loading blob from persistence: %w", err)
}

// ensureLDevID ensures an LDevID key exists using blob storage at the specified path.
// The Storage Root Key (srk) is used as the parent for the LDevID.
func (t *Client) ensureLDevID() (*tpm2.NamedHandle, error) {
	var err error
	t.ldevid, err = t.ensureKey(t.persistence.loadLDevIDBlob, t.persistence.saveLDevIDBlob, LDevIDTemplate)
	if err != nil {
		return nil, fmt.Errorf("ensuring ldevid: %w", err)
	}
	return t.ldevid, nil
}

// checkStorageHierarchyAuthStatus checks if the storage hierarchy has a password set
// using TPM GetCapabilities command. Returns true if a password is set.
func (t *Client) checkStorageHierarchyAuthStatus() (bool, error) {
	getCapCmd := tpm2.GetCapability{
		Capability:    tpm2.TPMCapTPMProperties,
		Property:      uint32(tpm2.TPMPTPermanent),
		PropertyCount: 1,
	}

	rsp, err := getCapCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return false, fmt.Errorf("getting TPM capabilities: %w", err)
	}

	data, err := rsp.CapabilityData.Data.TPMProperties()
	if err != nil {
		return false, fmt.Errorf("parsing properties: %w", err)
	}
	for _, prop := range data.TPMProperty {
		if prop.Property == tpm2.TPMPTPermanent {
			// ownerAuthSet is bit 0 of this value.
			return prop.Value&0x1 != 0, nil
		}
	}
	return false, fmt.Errorf("no valid properties found")
}

func (t *Client) generateStoragePassword() ([]byte, error) {
	// Use TPM's hardware random number generator for 32-byte password
	getRandCmd := tpm2.GetRandom{
		BytesRequested: 32,
	}

	rsp, err := getRandCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, fmt.Errorf("generating TPM random password: %w", err)
	}

	if len(rsp.RandomBytes.Buffer) != 32 {
		return nil, fmt.Errorf("TPM returned %d bytes, expected 32", len(rsp.RandomBytes.Buffer))
	}

	return rsp.RandomBytes.Buffer, nil
}

func (t *Client) changeStorageHierarchyPassword(currentPassword []byte, newPassword []byte) error {
	changeAuthCmd := tpm2.HierarchyChangeAuth{
		AuthHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHOwner,
			Auth:   tpm2.PasswordAuth(currentPassword),
		},
		NewAuth: tpm2.TPM2BAuth{Buffer: newPassword},
	}

	_, err := changeAuthCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return fmt.Errorf("setting storage hierarchy password: %w", err)
	}

	return nil
}
