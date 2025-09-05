package tpm

import (
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

const (
	transientHandleMin  = tpm2.TPMHandle(0x80000000)
	transientHandleMax  = tpm2.TPMHandle(0x80FFFFFF)
	persistentHandleMin = tpm2.TPMHandle(0x81000000)
	persistentHandleMax = tpm2.TPMHandle(0x81FFFFFF)
	nvReadChunkSize     = uint16(1024) // Maximum chunk size for NVRead operations
)

// tpmSession implements the Session interface
type tpmSession struct {
	conn        io.ReadWriteCloser
	storage     Storage
	log         *log.PrefixLogger
	authEnabled bool
	keyAlgo     KeyAlgorithm

	// Active handles
	handles map[KeyType]*tpm2.NamedHandle
	srk     *tpm2.NamedHandle
}

// NewSession creates a new TPM session
func NewSession(conn io.ReadWriteCloser, rw fileio.ReadWriter, log *log.PrefixLogger, authEnabled bool, persistencePath string, keyAlgo KeyAlgorithm) (Session, error) {
	session := &tpmSession{
		conn:        conn,
		storage:     NewFileStorage(rw, persistencePath, log),
		log:         log,
		authEnabled: authEnabled,
		keyAlgo:     keyAlgo,
		handles:     make(map[KeyType]*tpm2.NamedHandle),
	}

	// initialize the session by ensuring SRK and setting up auth
	if err := session.initialize(); err != nil {
		return nil, fmt.Errorf("initializing TPM session: %w", err)
	}

	return session, nil
}

func (s *tpmSession) initialize() error {
	s.log.Debug("Initializing TPM session")
	if err := s.ensureStorageAuth(); err != nil {
		return fmt.Errorf("setting up storage auth: %w", err)
	}

	// create/load SRK
	srkHandle, err := s.ensureSRK()
	if err != nil {
		return fmt.Errorf("ensuring SRK: %w", err)
	}
	s.srk = srkHandle
	s.handles[SRK] = srkHandle

	if err := s.loadExistingKeys(); err != nil {
		return fmt.Errorf("loading existing keys: %w", err)
	}

	return nil
}

func (s *tpmSession) GetHandle(keyType KeyType) (*tpm2.NamedHandle, error) {
	handle, exists := s.handles[keyType]
	if !exists {
		return nil, fmt.Errorf("handle not found for key type %s", keyType)
	}
	return handle, nil
}

func (s *tpmSession) CreateKey(keyType KeyType) (*tpm2.CreateResponse, error) {
	// ensure SRK is available
	if err := s.ensureSRKIsLoaded(); err != nil {
		return nil, fmt.Errorf("ensuring SRK is loaded: %w", err)
	}

	template, err := s.getKeyTemplate(keyType)
	if err != nil {
		return nil, fmt.Errorf("getting key template: %w", err)
	}

	createCmd := tpm2.Create{
		ParentHandle: *s.srk,
		InPublic:     tpm2.New2B(template),
	}

	createRsp, err := createCmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("executing %s %s create command: %w", s.keyAlgo, keyType, err)
	}

	if err := s.storage.StoreKey(keyType, createRsp.OutPublic, createRsp.OutPrivate); err != nil {
		return nil, fmt.Errorf("storing created key: %w", err)
	}

	return createRsp, nil
}

func (s *tpmSession) LoadKey(keyType KeyType) (*tpm2.NamedHandle, error) {
	if handle, exists := s.handles[keyType]; exists {
		s.log.Debugf("Key %s already loaded, handle=0x%x", keyType, handle.Handle)
		return handle, nil
	}

	if err := s.ensureSRKIsLoaded(); err != nil {
		return nil, fmt.Errorf("ensuring SRK is loaded: %w", err)
	}

	s.log.Debugf("Loading key %s from storage", keyType)
	pub, priv, err := s.storage.GetKey(keyType)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// key does not exist, create it
			s.log.Debugf("Key %s not found in storage, creating new key", keyType)
			_, err := s.CreateKey(keyType)
			if err != nil {
				return nil, fmt.Errorf("creating missing key: %w", err)
			}
			// retry getting the key
			pub, priv, err = s.storage.GetKey(keyType)
			if err != nil {
				return nil, fmt.Errorf("getting newly created key: %w", err)
			}
			if pub == nil || priv == nil {
				return nil, fmt.Errorf("newly created key %s is still nil after storage", keyType)
			}
			s.log.Debugf("Successfully created and stored key %s", keyType)
		} else {
			return nil, fmt.Errorf("getting key from storage: %w", err)
		}
	} else if pub == nil || priv == nil {
		// shouldn't happen but handle it anyway
		return nil, fmt.Errorf("key %s returned nil blobs without error", keyType)
	} else {
		s.log.Debugf("Key %s loaded from storage successfully", keyType)
	}

	loadCmd := tpm2.Load{
		ParentHandle: *s.srk,
		InPrivate:    *priv,
		InPublic:     *pub,
	}

	resp, err := loadCmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("loading key into TPM: %w", err)
	}

	handle := &tpm2.NamedHandle{
		Handle: resp.ObjectHandle,
		Name:   resp.Name,
	}

	s.handles[keyType] = handle
	s.log.Debugf("Successfully loaded key %s into TPM, handle=0x%x", keyType, handle.Handle)
	return handle, nil
}

func (s *tpmSession) CertifyKey(keyType KeyType, qualifyingData []byte) (certifyInfo, signature []byte, err error) {
	// get target handle to certify
	targetHandle, err := s.LoadKey(keyType)
	if err != nil {
		return nil, nil, fmt.Errorf("loading target key: %w", err)
	}

	// determine the signing key based on what we're certifying
	var signingHandle *tpm2.NamedHandle
	// We don't create our keys with any auth. If that changes we need to update this
	auth := tpm2.PasswordAuth(nil)

	// use LAK as the signing key for all certifications
	lakHandle, err := s.LoadKey(LAK)
	if err != nil {
		return nil, nil, fmt.Errorf("loading LAK for certification: %w", err)
	}
	signingHandle = lakHandle

	// create signature scheme
	sigScheme := tpm2.TPMTSigScheme{
		Scheme: tpm2.TPMAlgECDSA,
		Details: tpm2.NewTPMUSigScheme(
			tpm2.TPMAlgECDSA, // TODO: do not assume
			&tpm2.TPMSSchemeHash{HashAlg: tpm2.TPMAlgSHA256},
		),
	}

	cmd := tpm2.Certify{
		ObjectHandle: tpm2.AuthHandle{
			Handle: targetHandle.Handle,
			Name:   targetHandle.Name,
			Auth:   auth,
		},
		SignHandle: tpm2.AuthHandle{
			Handle: signingHandle.Handle,
			Name:   signingHandle.Name,
			Auth:   auth,
		},
		QualifyingData: tpm2.TPM2BData{Buffer: qualifyingData},
		InScheme:       sigScheme,
	}

	resp, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, nil, fmt.Errorf("TPM2_Certify failed: %w", err)
	}

	certifyInfoBytes := tpm2.Marshal(resp.CertifyInfo)
	signatureBytes := tpm2.Marshal(resp.Signature)

	return certifyInfoBytes, signatureBytes, nil
}

func (s *tpmSession) Sign(keyType KeyType, digest []byte) ([]byte, error) {
	handle, err := s.LoadKey(keyType)
	if err != nil {
		return nil, fmt.Errorf("loading signing key: %w", err)
	}

	cmd := tpm2.Sign{
		KeyHandle: *handle,
		Digest: tpm2.TPM2BDigest{
			Buffer: digest,
		},
		Validation: tpm2.TPMTTKHashCheck{
			Tag: tpm2.TPMSTHashCheck,
		},
	}

	resp, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("sign command failed for %s (digest size: %d): %w", keyType, len(digest), err)
	}

	// convert TPM signature to ASN.1 DER
	signature, err := ConvertTPMSignatureToDER(&resp.Signature)
	if err != nil {
		return nil, fmt.Errorf("converting TPM signature to ASN.1 DER: %w", err)
	}

	return signature, nil
}

// ConvertTPMSignatureToDER handles TPM2 signatures for RSA and ECDSA keys.
func ConvertTPMSignatureToDER(sig *tpm2.TPMTSignature) ([]byte, error) {
	if rsaSig, err := sig.Signature.RSASSA(); err == nil {
		// TPM RSA signatures are raw digest bytes
		return rsaSig.Sig.Buffer, nil
	}

	if ecdsaSig, err := sig.Signature.ECDSA(); err == nil {
		r := new(big.Int).SetBytes(ecdsaSig.SignatureR.Buffer)
		s := new(big.Int).SetBytes(ecdsaSig.SignatureS.Buffer)

		return asn1.Marshal(struct {
			R, S *big.Int
		}{R: r, S: s})
	}

	return nil, errors.New("unsupported or unrecognized TPM signature algorithm")
}

func (s *tpmSession) GetPublicKey(keyType KeyType) (*tpm2.TPM2B[tpm2.TPMTPublic, *tpm2.TPMTPublic], error) {
	handle, err := s.LoadKey(keyType)
	if err != nil {
		return nil, fmt.Errorf("loading key: %w", err)
	}

	pub, err := tpm2.ReadPublic{
		ObjectHandle: handle.Handle,
	}.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("reading public key: %w", err)
	}

	return &pub.OutPublic, nil
}

func (s *tpmSession) Clear() error {
	// only the lockout and platform hierarchies can invoke tpm2.Clear
	// it is possible to block the lockout hierarchy from performing a clear operation
	// it is possible to add passwords to both the lockout and platform hierarchies
	// We make a best effort to invoke clear, but if those operations fail, we attempt
	// to reset owner password and make our keys unrecoverable.
	hierarchies := []struct {
		name   string
		handle tpm2.TPMHandle
	}{
		{
			name:   "lockout",
			handle: tpm2.TPMRHLockout,
		},
		{
			name:   "platform",
			handle: tpm2.TPMRHPlatform,
		},
	}

	var errs []error
	for _, hier := range hierarchies {
		cmd := tpm2.Clear{
			AuthHandle: tpm2.AuthHandle{
				Handle: hier.handle,
				Auth:   tpm2.PasswordAuth(nil),
			},
		}

		_, err := cmd.Execute(transport.FromReadWriter(s.conn))
		if err != nil {
			errs = append(errs, fmt.Errorf("clearing hierarchy %q: %w", hier.name, err))
		}
	}

	// if all commands failed we failed to invoke clear
	// so we try to clean up as much as we manually can
	// but still treat everything as if it errored.
	if len(errs) == len(hierarchies) {
		// reset the error into a compact one
		errs = []error{
			fmt.Errorf("clearing hierarchy failed: %w", errors.Join(errs...)),
		}
		if err := s.resetStorageHierarchyPassword(); err != nil {
			// if we fail to reset the storage password something is very off. We shouldn't erase our
			// password in case we can try again.
			return fmt.Errorf("resetting storage hierarchy password: %w %w", err, errors.Join(errs...))
		}
		if flushErrs := s.flushKeys(); len(flushErrs) != 0 {
			errs = append(errs, fmt.Errorf("flushing hierarchy keys: %w", errors.Join(flushErrs...)))
		}
	} else {
		// if any of the above commands succeeded we consider the operation successful
		errs = nil
	}

	// clear internal state
	s.handles = make(map[KeyType]*tpm2.NamedHandle)
	s.srk = nil

	if err := s.storage.ClearPassword(); err != nil {
		errs = append(errs, fmt.Errorf("clearing stored password: %w", err))
	}
	// Clear stored keys by storing empty values
	if err := s.clearStoredKeys(); err != nil {
		errs = append(errs, fmt.Errorf("clearing stored keys: %w", err))
	}

	if len(errs) != 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (s *tpmSession) resetStorageHierarchyPassword() error {
	currentPassword, err := s.storage.GetPassword()
	if err != nil {
		// If no password is set we assume ownership isn't enabled
		// and thus no reason to reset the password
		return nil
	}

	// TODO: we only call this method in the case that we are unable to successfully call tpm2_clear
	// We delete our keys and password so that they are unrecoverable to our application, but
	// they are technically still usable by the TPM. We could update the hierarchy password to something
	// random and make the keys unusable by everyone, but that would effectively brick the device until someone
	// comes and clears it manually (perhaps desirable). For now, leave it in a state where we could restart the enrollment
	// process if necessary.
	if err := s.updateStorageHierarchyPassword(currentPassword, nil); err != nil {
		return fmt.Errorf("updating storage hierarchy password: %w", err)
	}

	return nil
}

func (s *tpmSession) flushKeys() []error {
	var errs []error

	// Flush all known handles
	for keyType, handle := range s.handles {
		if err := s.flushHandle(handle); err != nil {
			errs = append(errs, fmt.Errorf("flushing %s: %w", keyType, err))
		}
		delete(s.handles, keyType)
	}
	// we flush the SRK above
	s.srk = nil

	return errs
}

func (s *tpmSession) clearStoredKeys() error {
	// Clear stored keys by removing them from storage
	// This makes them unrecoverable even if TPM Clear failed
	keyTypes := []KeyType{LDevID, LAK}

	for _, kt := range keyTypes {
		_ = s.storage.ClearKey(kt)
	}

	return nil
}

func (s *tpmSession) Close() error {
	var errs []error

	// flush all handles we know about
	for keyType, handle := range s.handles {
		if err := s.flushHandle(handle); err != nil {
			errs = append(errs, fmt.Errorf("flushing %s handle: %w", keyType, err))
		}
	}

	s.handles = make(map[KeyType]*tpm2.NamedHandle)
	s.srk = nil

	// close the TPM connection
	if s.conn != nil {
		if err := s.conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing TPM connection: %w", err))
		}
		s.conn = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during session close: %v", errs)
	}
	return nil
}

// isEKCertPresent checks if an EK certificate exists at the given NVRAM index
// without reading the actual certificate data
func (s *tpmSession) isEKCertPresent(nvIndex uint32) bool {
	readPublicCmd := tpm2.NVReadPublic{
		NVIndex: tpm2.TPMHandle(nvIndex),
	}

	publicResp, err := readPublicCmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return false // Index doesn't exist or not accessible
	}

	nvPublic, err := publicResp.NVPublic.Contents()
	if err != nil {
		return false
	}

	return nvPublic.DataSize > 0 // Has actual data
}

// detectEKAlgorithm determines the EK algorithm based on which certificate
// exists in NVRAM, prioritizing RSA first
func (s *tpmSession) detectEKAlgorithm() (KeyAlgorithm, error) {
	if s.isEKCertPresent(client.EKCertNVIndexRSA) {
		return RSA, nil
	}

	if s.isEKCertPresent(client.EKCertNVIndexECC) {
		return ECDSA, nil
	}

	return "", fmt.Errorf("no EK certificate found in NVRAM")
}

// endorsementKeyCert reads endorsement key certificate from TPM NVRAM using direct commands
func (s *tpmSession) endorsementKeyCert() ([]byte, error) {
	if s.conn == nil {
		return nil, fmt.Errorf("cannot read endorsement key certificate: no connection available")
	}

	// Detect which EK certificate type is available
	ekAlgo, err := s.detectEKAlgorithm()
	if err != nil {
		return nil, fmt.Errorf("no endorsement key certificate found: %w", err)
	}

	// Read the certificate from the appropriate index
	var nvIndex uint32
	switch ekAlgo {
	case RSA:
		nvIndex = client.EKCertNVIndexRSA
	case ECDSA:
		nvIndex = client.EKCertNVIndexECC
	default:
		return nil, fmt.Errorf("unsupported EK algorithm: %s", ekAlgo)
	}

	certData, err := s.readEKCertFromNVRAM(nvIndex)
	if err != nil {
		return nil, fmt.Errorf("reading %s EK certificate from NVRAM: %w", ekAlgo, err)
	}

	if len(certData) == 0 {
		return nil, fmt.Errorf("endorsement key certificate is empty")
	}

	s.log.Debugf("Successfully read %s EK certificate: %d bytes", ekAlgo, len(certData))
	return certData, nil
}

// readEKCertFromNVRAM reads a certificate from the specified NVRAM index using Owner hierarchy
func (s *tpmSession) readEKCertFromNVRAM(nvIndex uint32) ([]byte, error) {
	// First check if the NVRAM index exists and get its size
	readPublicCmd := tpm2.NVReadPublic{
		NVIndex: tpm2.TPMHandle(nvIndex),
	}

	publicResp, err := readPublicCmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("NVRAM index 0x%x does not exist or is not accessible: %w", nvIndex, err)
	}

	nvPublic, err := publicResp.NVPublic.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to get NVRAM public contents: %w", err)
	}

	if nvPublic.DataSize == 0 {
		return nil, fmt.Errorf("NVRAM index 0x%x is empty", nvIndex)
	}

	var password []byte
	if s.authEnabled {
		password, err = s.storage.GetPassword()
		if err != nil {
			return nil, fmt.Errorf("failed to read auth password: %w", err)
		}
	}

	// Read the certificate data using Owner hierarchy with chunking
	var certData []byte
	dataSize := nvPublic.DataSize

	for offset := uint16(0); offset < dataSize; offset += nvReadChunkSize {
		chunkSize := nvReadChunkSize
		if offset+chunkSize > dataSize {
			chunkSize = dataSize - offset
		}

		readCmd := tpm2.NVRead{
			AuthHandle: tpm2.AuthHandle{
				Handle: tpm2.TPMRHOwner,
				Auth:   tpm2.PasswordAuth(password),
			},
			NVIndex: tpm2.NamedHandle{
				Handle: tpm2.TPMHandle(nvIndex),
				Name:   publicResp.NVName,
			},
			Size:   chunkSize,
			Offset: offset,
		}

		resp, err := readCmd.Execute(transport.FromReadWriter(s.conn))
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk at offset %d from NVRAM index 0x%x: %w", offset, nvIndex, err)
		}

		certData = append(certData, resp.Data.Buffer...)
	}

	return certData, nil
}

func (s *tpmSession) GetEndorsementKeyCert() ([]byte, error) {
	certData, err := s.endorsementKeyCert()
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}

	if len(certData) == 0 {
		s.log.Warnf("TPM Endorsement Key certificate is empty - this TPM may not have an embedded EK certificate")
		return nil, fmt.Errorf("endorsement key certificate is empty - this TPM may not have an embedded EK certificate")
	}

	return certData, nil
}

func (s *tpmSession) ensureStorageAuth() error {
	if !s.authEnabled {
		s.log.Info("TPM Authentication is disabled")
		return nil
	}

	isAuthSet, err := s.isStorageHierarchyAuthSet()
	if err != nil {
		return fmt.Errorf("checking storage hierarchy auth status: %w", err)
	}

	if isAuthSet {
		s.log.Info("TPM Authentication is enabled")
		return nil
	}

	password, err := s.generateStoragePassword()
	if err != nil {
		return fmt.Errorf("generating storage hierarchy password: %w", err)
	}

	// store password to disk before setting it in TPM
	if err := s.storage.StorePassword(password); err != nil {
		return fmt.Errorf("storing password: %w", err)
	}

	if err := s.updateStorageHierarchyPassword(nil, password); err != nil {
		if clearErr := s.storage.ClearPassword(); clearErr != nil {
			return fmt.Errorf("setting storage hierarchy password: %w; clearing persisted password: %v", err, clearErr)
		}
		return fmt.Errorf("setting storage hierarchy password: %w", err)
	}

	return nil
}

func (s *tpmSession) ensureSRK() (*tpm2.NamedHandle, error) {
	password, err := s.getPassword()
	if err != nil {
		// no password, use nil (disabled)
		password = nil
	}

	var template tpm2.TPMTPublic
	switch s.keyAlgo {
	case ECDSA:
		template = tpm2.ECCSRKTemplate
	case RSA:
		template = tpm2.RSASRKTemplate
	default:
		return nil, fmt.Errorf("unsupported key algorithm: %s", s.keyAlgo)
	}

	cmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHOwner,
			Auth:   tpm2.PasswordAuth(password),
		},
		InPublic: tpm2.New2B(template),
	}

	resp, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("creating SRK primary: %w", err)
	}

	return &tpm2.NamedHandle{
		Handle: resp.ObjectHandle,
		Name:   resp.Name,
	}, nil
}

// ensureSRKIsLoaded checks if the SRK handle is still valid in the TPM
// and regenerates it if it was flushed by aggressive cleanup
func (s *tpmSession) ensureSRKIsLoaded() error {
	if s.srk == nil {
		// SRK was never created, create it now
		srkHandle, err := s.ensureSRK()
		if err != nil {
			return fmt.Errorf("creating SRK: %w", err)
		}
		s.srk = srkHandle
		s.handles[SRK] = srkHandle
		return nil
	}

	// check if the SRK handle is still valid by trying to read its public key
	cmd := tpm2.ReadPublic{
		ObjectHandle: s.srk.Handle,
	}

	_, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err == nil {
		// SRK is still valid
		return nil
	}

	// SRK handle is invalid, regenerate
	s.log.Debugf("SRK handle is invalid (possibly flushed), regenerating...")
	srkHandle, err := s.ensureSRK()
	if err != nil {
		return fmt.Errorf("regenerating SRK: %w", err)
	}

	s.srk = srkHandle
	s.handles[SRK] = srkHandle

	// clear any cached child key handles since they're now invalid too
	for keyType, handle := range s.handles {
		if keyType != SRK {
			_ = s.flushHandle(handle)
			delete(s.handles, keyType)
		}
	}

	s.log.Debugf("SRK regenerated successfully")
	return nil
}

func (s *tpmSession) loadExistingKeys() error {
	// try to load LDevID and LAK
	keyTypes := []KeyType{LDevID, LAK}
	for _, keyType := range keyTypes {
		_, err := s.LoadKey(keyType)
		if err != nil {
			s.log.Debugf("Could not load existing %s key will generate: %v", keyType, err)
		}
	}

	return nil
}

func (s *tpmSession) getPassword() ([]byte, error) {
	if !s.authEnabled {
		return nil, nil
	}
	return s.storage.GetPassword()
}

func (s *tpmSession) getKeyTemplate(keyType KeyType) (tpm2.TPMTPublic, error) {
	switch keyType {
	case LDevID:
		return LDevIDTemplate(s.keyAlgo)
	case LAK:
		return AttestationKeyTemplate(s.keyAlgo)
	default:
		return tpm2.TPMTPublic{}, fmt.Errorf("unsupported key type: %s", keyType)
	}
}

func (s *tpmSession) flushHandle(handle *tpm2.NamedHandle) error {
	if handle == nil {
		return nil
	}

	if handle.Handle < persistentHandleMin || handle.Handle > persistentHandleMax {
		flushCmd := tpm2.FlushContext{
			FlushHandle: handle.Handle,
		}

		_, err := flushCmd.Execute(transport.FromReadWriter(s.conn))
		if err != nil {
			return fmt.Errorf("flushing context for handle 0x%x: %w", handle.Handle, err)
		}
	}
	return nil
}

// FlushAllTransientHandles aggressively flushes all transient handles in the TPM
// This helps clean up any handles that might have been created by go-tpm-tools or other libraries
// It preserves handles that are actively tracked by this session
func (s *tpmSession) FlushAllTransientHandles() error {
	// Create a set of handles we want to preserve
	preserveHandles := make(map[tpm2.TPMHandle]bool)
	for _, handle := range s.handles {
		if handle != nil {
			preserveHandles[handle.Handle] = true
		}
	}

	// get all loaded handles from the TPM
	cmd := tpm2.GetCapability{
		Capability:    tpm2.TPMCapHandles,
		Property:      uint32(tpm2.TPMHTTransient) << 24, // transient handles start at 0x80000000
		PropertyCount: 256,                               // maximum number of handles to retrieve
	}

	resp, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		// note: if we can't get capabilities, that's not necessarily an error
		// the TPM might just not have any transient handles
		s.log.Debugf("Could not get transient handles for cleanup: %v", err)
		return nil
	}

	if resp.CapabilityData.Capability != tpm2.TPMCapHandles {
		return nil
	}

	handles, err := resp.CapabilityData.Data.Handles()
	if err != nil {
		s.log.Debugf("Could not parse handle list: %v", err)
		return nil
	}

	var flushErrors []error
	flushedCount := 0
	for _, handle := range handles.Handle {
		// only flush transient handles (0x80000000 - 0x8FFFFFFF)
		if handle >= transientHandleMin && handle <= transientHandleMax {
			if preserveHandles[handle] {
				s.log.Debugf("Preserving active session handle 0x%x", handle)
				continue
			}

			flushCmd := tpm2.FlushContext{
				FlushHandle: handle,
			}

			_, err := flushCmd.Execute(transport.FromReadWriter(s.conn))
			if err != nil {
				flushErrors = append(flushErrors, fmt.Errorf("flushing transient handle 0x%x: %w", handle, err))
				continue
			}
			flushedCount++
			s.log.Debugf("Flushed unused transient handle 0x%x", handle)
		}
	}

	if flushedCount > 0 {
		s.log.Debugf("Flushed %d unused transient handles", flushedCount)
	}

	if len(flushErrors) > 0 {
		return fmt.Errorf("errors flushing transient handles: %v", flushErrors)
	}

	return nil
}

func (s *tpmSession) isStorageHierarchyAuthSet() (bool, error) {
	cmd := tpm2.GetCapability{
		Capability:    tpm2.TPMCapTPMProperties,
		Property:      uint32(tpm2.TPMPTPermanent),
		PropertyCount: 1,
	}

	resp, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return false, fmt.Errorf("getting TPM capabilities: %w", err)
	}

	data, err := resp.CapabilityData.Data.TPMProperties()
	if err != nil {
		return false, fmt.Errorf("parsing TPM properties: %w", err)
	}

	for _, prop := range data.TPMProperty {
		if prop.Property == tpm2.TPMPTPermanent {
			// ownerAuthSet is bit 0 of TPM_PT_PERMANENT per TCG spec
			return prop.Value&0x1 != 0, nil
		}
	}

	return false, fmt.Errorf("TPM_PT_PERMANENT property not found")
}

func (s *tpmSession) generateStoragePassword() ([]byte, error) {
	cmd := tpm2.GetRandom{
		BytesRequested: 32,
	}

	resp, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("executing TPM GetRandom command: %w", err)
	}

	if len(resp.RandomBytes.Buffer) != 32 {
		return nil, fmt.Errorf("TPM returned %d bytes, expected 32", len(resp.RandomBytes.Buffer))
	}

	return resp.RandomBytes.Buffer, nil
}

func (s *tpmSession) updateStorageHierarchyPassword(currentPassword, newPassword []byte) error {
	changeAuthCmd := tpm2.HierarchyChangeAuth{
		AuthHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHOwner,
			Auth:   tpm2.PasswordAuth(currentPassword),
		},
		NewAuth: tpm2.TPM2BAuth{Buffer: newPassword},
	}

	_, err := changeAuthCmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return fmt.Errorf("setting storage hierarchy password: %w", err)
	}

	return nil
}

// ekPolicy implements the policy callback for Endorsement Key authorization
// This authorizes the use of EK by executing PolicySecret with the Endorsement hierarchy
func ekPolicy(t transport.TPM, handle tpm2.TPMISHPolicy, nonceTPM tpm2.TPM2BNonce) error {
	cmd := tpm2.PolicySecret{
		AuthHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHEndorsement,
			Auth:   tpm2.PasswordAuth(nil),
		},
		PolicySession: handle,
		NonceTPM:      nonceTPM,
	}
	_, err := cmd.Execute(t)
	return err
}

func (s *tpmSession) GenerateChallenge(secret []byte) ([]byte, []byte, error) {
	ekAlgo, err := s.detectEKAlgorithm()
	if err != nil {
		return nil, nil, fmt.Errorf("detecting EK algorithm: %w", err)
	}

	var template tpm2.TPMTPublic
	switch ekAlgo {
	case ECDSA:
		template = tpm2.ECCEKTemplate
	case RSA:
		template = tpm2.RSAEKTemplate
	default:
		return nil, nil, fmt.Errorf("unsupported key algorithm for EK: %s", ekAlgo)
	}

	cmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHEndorsement,
		InPublic:      tpm2.New2B(template),
	}

	resp, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, nil, fmt.Errorf("creating EK primary: %w", err)
	}

	ekHandle := &tpm2.NamedHandle{
		Handle: resp.ObjectHandle,
		Name:   resp.Name,
	}
	defer func() { _ = s.flushHandle(ekHandle) }()

	lakHandle, err := s.LoadKey(LAK)
	if err != nil {
		return nil, nil, fmt.Errorf("loading LAK: %w", err)
	}

	makeCred := tpm2.MakeCredential{
		Handle:     ekHandle.Handle,
		Credential: tpm2.TPM2BDigest{Buffer: secret},
		ObjectName: lakHandle.Name,
	}
	makeResp, err := makeCred.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, nil, fmt.Errorf("TPM2_MakeCredential failed: %w", err)
	}

	return makeResp.CredentialBlob.Buffer, makeResp.Secret.Buffer, nil
}

// SolveChallenge uses TPM2_ActivateCredential to decrypt a credential challenge using the LAK as the ActivateHandle
func (s *tpmSession) SolveChallenge(credentialBlob, encryptedSecret []byte) ([]byte, error) {
	if len(credentialBlob) == 0 {
		return nil, fmt.Errorf("credential blob is empty")
	}
	if len(encryptedSecret) == 0 {
		return nil, fmt.Errorf("encrypted secret is empty")
	}

	ekAlgo, err := s.detectEKAlgorithm()
	if err != nil {
		return nil, fmt.Errorf("detecting EK algorithm: %w", err)
	}

	var template tpm2.TPMTPublic
	switch ekAlgo {
	case ECDSA:
		template = tpm2.ECCEKTemplate
	case RSA:
		template = tpm2.RSAEKTemplate
	default:
		return nil, fmt.Errorf("unsupported key algorithm for EK: %s", ekAlgo)
	}

	cmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHEndorsement,
		InPublic:      tpm2.New2B(template),
	}

	resp, err := cmd.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("creating EK primary: %w", err)
	}

	ekHandle := &tpm2.NamedHandle{
		Handle: resp.ObjectHandle,
		Name:   resp.Name,
	}
	defer func() { _ = s.flushHandle(ekHandle) }()

	lakHandle, err := s.LoadKey(LAK)
	if err != nil {
		return nil, fmt.Errorf("loading LAK: %w", err)
	}

	activate := tpm2.ActivateCredential{
		ActivateHandle: *lakHandle,
		KeyHandle: tpm2.AuthHandle{
			Handle: ekHandle.Handle,
			Name:   ekHandle.Name,
			// Activating with the EK requires usage of a policy. This policy is derived from go-tpm
			Auth: tpm2.Policy(tpm2.TPMAlgSHA256, 16, ekPolicy),
		},
		CredentialBlob: tpm2.TPM2BIDObject{Buffer: credentialBlob},
		Secret:         tpm2.TPM2BEncryptedSecret{Buffer: encryptedSecret},
	}

	activateResp, err := activate.Execute(transport.FromReadWriter(s.conn))
	if err != nil {
		return nil, fmt.Errorf("TPM2_ActivateCredential failed: %w", err)
	}

	return activateResp.CertInfo.Buffer, nil
}
