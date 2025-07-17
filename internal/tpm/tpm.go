package tpm

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/google/go-tpm-tools/client"
	pbattest "github.com/google/go-tpm-tools/proto/attest"
	pbtpm "github.com/google/go-tpm-tools/proto/tpm"
	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
	"github.com/google/go-tpm/tpmutil"
	"sigs.k8s.io/yaml"
)

// ErrEmptyFile is returned when a file is expected to contain data but is empty.
var ErrEmptyFile = errors.New("file is empty")

const (
	// MinNonceLength is the minimum required length for nonces used in TPM operations.
	MinNonceLength = 8

	// TPM Handle Ranges
	// PersistentHandleMin is the minimum valid persistent handle value.
	PersistentHandleMin = tpm2.TPMHandle(0x81000000)
	// PersistentHandleMax is the maximum valid persistent handle value.
	PersistentHandleMax = tpm2.TPMHandle(0x81FFFFFF)
)

func validatePersistentHandle(handle tpm2.TPMHandle) error {
	if handle < PersistentHandleMin || handle > PersistentHandleMax {
		return fmt.Errorf("handle 0x%x is not in valid persistent range 0x%x-0x%x", handle, PersistentHandleMin, PersistentHandleMax)
	}
	return nil
}

// Capabilities represents TPM capabilities and resource information.
type Capabilities struct {
	// PersistentHandleCount is the total number of persistent handles supported by the TPM.
	PersistentHandleCount uint32
	// PersistentHandleAvailCount is the number of available persistent handles.
	PersistentHandleAvailCount uint32
}

// Device represents a TPM device and its associated file paths.
type Device struct {
	// DeviceNumber is the numeric identifier of the TPM device (e.g., "0" for /dev/tpm0).
	DeviceNumber string
	// DevicePath is the full path to the TPM device file (e.g., "/dev/tpm0").
	DevicePath string
	// ResourceMgrPath is the path to the TPM resource manager (e.g., "/dev/tpmrm0").
	ResourceMgrPath string
	// VersionPath is the path to the TPM version file in sysfs.
	VersionPath string
	// SysfsPath is the path to the TPM device directory in sysfs.
	SysfsPath string
	tpm       *TPM
	rw        fileio.ReadWriter
}

// ldevIDBlob represents a serialized LDevID key pair for storage.
type ldevIDBlob struct {
	// PublicBlob contains the serialized public key data.
	PublicBlob []byte `yaml:"public"`
	// PrivateBlob contains the serialized private key data.
	PrivateBlob []byte `yaml:"private"`
}

type ldevIDStrategy interface {
	execute(t *TPM, srk tpm2.NamedHandle) (*tpm2.NamedHandle, error)
}

type persistentHandleStrategy struct {
	handle tpm2.TPMHandle
}

type blobStorageStrategy struct {
	path       string
	readWriter fileio.ReadWriter
}

type persistentPathStrategy struct {
	path       string
	readWriter fileio.ReadWriter
}

type transientStrategy struct{}

type ensureLDevIDOptions struct {
	ensureStrategy ldevIDStrategy
}

// EnsureLDevIDOption is a functional option for configuring LDevID storage and persistence.
type EnsureLDevIDOption func(*ensureLDevIDOptions)

// WithPersistentHandle configures LDevID to use a specific persistent handle.
// The handle must be in the valid persistent handle range (0x81000000-0x81FFFFFF).
func WithPersistentHandle(handle uint32) EnsureLDevIDOption {
	return func(opts *ensureLDevIDOptions) {
		opts.ensureStrategy = &persistentHandleStrategy{
			handle: tpm2.TPMHandle(handle),
		}
	}
}

// WithBlobStorage configures LDevID to be stored as encrypted blobs in a file.
// The key material is encrypted by the TPM and can only be decrypted by the same TPM.
func WithBlobStorage(path string, readWriter fileio.ReadWriter) EnsureLDevIDOption {
	return func(opts *ensureLDevIDOptions) {
		opts.ensureStrategy = &blobStorageStrategy{
			path:       path,
			readWriter: readWriter,
		}
	}
}

// WithPersistentHandlePath configures LDevID to use an auto-managed persistent handle.
// The handle value is automatically selected and stored in the specified file.
func WithPersistentHandlePath(path string, readWriter fileio.ReadWriter) EnsureLDevIDOption {
	return func(opts *ensureLDevIDOptions) {
		opts.ensureStrategy = &persistentPathStrategy{
			path:       path,
			readWriter: readWriter,
		}
	}
}

// WithTransientKey configures LDevID to use a transient key that exists only in memory.
// The key will be lost when the TPM is reset or the application terminates.
func WithTransientKey() EnsureLDevIDOption {
	return func(opts *ensureLDevIDOptions) {
		opts.ensureStrategy = &transientStrategy{}
	}
}

// TPM represents a connection to a TPM device and manages TPM operations.
type TPM struct {
	devicePath string
	channel    io.ReadWriteCloser
	srk        *tpm2.NamedHandle
	ldevid     *tpm2.NamedHandle
}

// OpenTPM opens a connection to a TPM device at the specified path.
// It returns a TPM instance that can be used for TPM operations.
func OpenTPM(rw fileio.ReadWriter, devicePath string) (*TPM, error) {
	ch, err := tpmutil.OpenTPM(rw.PathFor(devicePath))
	if err != nil {
		return nil, err
	}
	return &TPM{devicePath: devicePath, channel: ch}, nil
}

// Close closes the TPM connection and flushes any transient handles.
// It should be called when the TPM is no longer needed to free resources.
func (t *TPM) Close() error {
	if t == nil {
		return nil
	}
	if t.channel == nil {
		return nil
	}

	var errs []error

	// Flush transient handles before closing
	if t.srk != nil {
		if err := t.FlushContextForHandle(t.srk.Handle); err != nil {
			errs = append(errs, fmt.Errorf("flushing SRK handle: %w", err))
		}
		t.srk = nil
	}

	if t.ldevid != nil {
		if err := t.FlushContextForHandle(t.ldevid.Handle); err != nil {
			errs = append(errs, fmt.Errorf("flushing LDevID handle: %w", err))
		}
		t.ldevid = nil
	}

	// Always close the channel
	if err := t.channel.Close(); err != nil {
		errs = append(errs, fmt.Errorf("closing TPM channel: %w", err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// VendorInfo returns the TPM manufacturer information.
// This can be used to identify the TPM vendor and model.
func (t *TPM) VendorInfo() ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("cannot get TPM vendor info: nil receiver in TPM struct")
	}
	if t.channel == nil {
		return nil, fmt.Errorf("cannot get TPM vendor info: no channel available in TPM struct")
	}
	return legacy.GetManufacturer(t.channel)
}

// ReadPCRValues reads PCR values from the TPM and populates the provided map.
// The map keys are formatted as "pcr01", "pcr02", etc., and values are hex-encoded.
func (t *TPM) ReadPCRValues(measurements map[string]string) error {
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

// GenerateSRKPrimary creates or recreates an ECC Primary Storage Root Key in the Owner/Storage Hierarchy.
// This key is deterministically generated from the Storage Primary Seed and input parameters.
func (t *TPM) GenerateSRKPrimary() (*tpm2.NamedHandle, error) {
	createPrimaryCmd := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHOwner,
		InPublic:      tpm2.New2B(tpm2.ECCSRKTemplate),
	}
	transportTPM := transport.FromReadWriter(t.channel)
	createPrimaryRsp, err := createPrimaryCmd.Execute(transportTPM)
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
func (t *TPM) CreateLAK() (*client.Key, error) {
	// AttestationKeyECC generates and loads a key from AKTemplateECC in the Owner (aka 'Storage') hierarchy.
	return client.AttestationKeyECC(t.channel)
}

// GetAttestation generates a TPM attestation using the provided nonce and attestation key.
// The nonce must be at least MinNonceLength bytes long for security.
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

// CreateLDevID creates a transient ECC LDevID key pair under the Storage/Owner hierarchy.
// The Storage Root Key (srk) is used as the parent key for the LDevID.
func (t *TPM) CreateLDevID(srk tpm2.NamedHandle) (*tpm2.NamedHandle, error) {
	createCmd := tpm2.Create{
		ParentHandle: srk,
		InPublic:     tpm2.New2B(LDevIDTemplate),
	}
	transportTPM := transport.FromReadWriter(t.channel)
	createRsp, err := createCmd.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("executing LDevID create command: %w", err)
	}
	loadCmd := tpm2.Load{
		ParentHandle: srk,
		InPrivate:    createRsp.OutPrivate,
		InPublic:     createRsp.OutPublic,
	}

	loadRsp, err := loadCmd.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("error loading ldevid key: %w", err)
	}

	t.ldevid = &tpm2.NamedHandle{
		Handle: loadRsp.ObjectHandle,
		Name:   loadRsp.Name,
	}

	return t.ldevid, nil
}

// loadLDevIDFromBlob will load a LDevID for the existing SRK from key blob parts
// According to https://trustedcomputinggroup.org/wp-content/uploads/TPM-2p0-Keys-for-Device-Identity-and-Attestation_v1_r12_pub10082021.pdf
// it is safe to persist these blobs are they can only be decrypted by the originating TPM
func (t *TPM) loadLDevIDFromBlob(public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) (*tpm2.NamedHandle, error) {
	loadCmd := tpm2.Load{
		ParentHandle: t.srk,
		InPrivate:    private,
		InPublic:     public,
	}

	loadRsp, err := loadCmd.Execute(transport.FromReadWriter(t.channel))
	if err != nil {
		return nil, fmt.Errorf("error loading ldevid key: %w", err)
	}

	t.ldevid = &tpm2.NamedHandle{
		Handle: loadRsp.ObjectHandle,
		Name:   loadRsp.Name,
	}
	return t.ldevid, nil
}

func (t *TPM) loadLDevIDFromHandle(handle tpm2.TPMHandle) (*tpm2.NamedHandle, error) {
	readPublicCmd := tpm2.ReadPublic{
		ObjectHandle: handle,
	}

	transportTPM := transport.FromReadWriter(t.channel)
	readPublicRsp, err := readPublicCmd.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("reading public area from handle 0x%x: %w", handle, err)
	}

	t.ldevid = &tpm2.NamedHandle{
		Handle: handle,
		Name:   readPublicRsp.Name,
	}

	return t.ldevid, nil
}

func (t *TPM) persistLDevID(ldevid *tpm2.NamedHandle, handle tpm2.TPMHandle) error {
	if ldevid == nil {
		return fmt.Errorf("cannot persist nil LDevID handle")
	}

	evictCmd := tpm2.EvictControl{
		Auth:             tpm2.TPMRHOwner,
		ObjectHandle:     *ldevid,
		PersistentHandle: handle,
	}

	transportTPM := transport.FromReadWriter(t.channel)
	_, err := evictCmd.Execute(transportTPM)
	if err != nil {
		return fmt.Errorf("persisting LDevID to handle 0x%x: %w", handle, err)
	}

	return nil
}

func (t *TPM) flushContext() error {
	if t.ldevid == nil {
		return nil
	}

	if err := t.FlushContextForHandle(t.ldevid.Handle); err != nil {
		return err
	}

	t.ldevid = nil
	return nil
}

// FlushContextForHandle flushes the TPM context for the specified handle if it's transient.
// Persistent handles are not flushed as they remain in the TPM across reboots.
func (t *TPM) FlushContextForHandle(handle tpm2.TPMHandle) error {
	// Only flush if this is a transient handle (not a persistent handle)
	if handle < PersistentHandleMin || handle > PersistentHandleMax {
		flushCmd := tpm2.FlushContext{
			FlushHandle: handle,
		}

		transportTPM := transport.FromReadWriter(t.channel)
		_, err := flushCmd.Execute(transportTPM)
		if err != nil {
			return fmt.Errorf("flushing context for handle 0x%x: %w", handle, err)
		}
	}
	return nil
}

func (t *TPM) loadLDevIDFromPersistentHandle(handle tpm2.TPMHandle) (*tpm2.NamedHandle, error) {
	readPublicCmd := tpm2.ReadPublic{
		ObjectHandle: handle,
	}

	transportTPM := transport.FromReadWriter(t.channel)
	readPublicRsp, err := readPublicCmd.Execute(transportTPM)
	if err != nil {
		return nil, fmt.Errorf("reading public area from persistent handle 0x%x: %w", handle, err)
	}

	t.ldevid = &tpm2.NamedHandle{
		Handle: handle,
		Name:   readPublicRsp.Name,
	}

	return t.ldevid, nil
}

// GetQuote generates a TPM quote using the provided nonce, attestation key, and PCR selection.
// The quote provides cryptographic evidence of the current PCR values.
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

// Capabilities returns TPM capability information including persistent handle counts.
// This is useful for determining available TPM resources.
func (t *TPM) Capabilities() (*Capabilities, error) {
	if t.channel == nil {
		return nil, fmt.Errorf("TPM channel not available")
	}

	transportTPM := transport.FromReadWriter(t.channel)
	caps := &Capabilities{}

	cmds := []tpm2.GetCapability{
		{
			Capability:    tpm2.TPMCapTPMProperties,
			Property:      uint32(tpm2.TPMPTHRPersistentAvail),
			PropertyCount: 1,
		}, {
			Capability:    tpm2.TPMCapTPMProperties,
			Property:      uint32(tpm2.TPMPTHRPersistent),
			PropertyCount: 1,
		},
	}

	for _, cmd := range cmds {
		capRsp, err := cmd.Execute(transportTPM)
		if err != nil {
			return nil, fmt.Errorf("querying TPM properties: %d : %w", cmd.Property, err)
		}
		tpmProperties, err := capRsp.CapabilityData.Data.TPMProperties()
		if err != nil {
			return nil, fmt.Errorf("parsing TPM properties: %w", err)
		}

		for _, prop := range tpmProperties.TPMProperty {
			switch prop.Property {
			case tpm2.TPMPTHRPersistentAvail:
				caps.PersistentHandleAvailCount = prop.Value
			case tpm2.TPMPTHRPersistent:
				caps.PersistentHandleCount = prop.Value
			}
		}
	}

	return caps, nil
}

func (t *TPM) validateLDevIDKey(handle tpm2.TPMHandle, srk *tpm2.NamedHandle) error {
	readPublicCmd := tpm2.ReadPublic{
		ObjectHandle: handle,
	}

	transportTPM := transport.FromReadWriter(t.channel)
	readPublicRsp, err := readPublicCmd.Execute(transportTPM)
	if err != nil {
		return fmt.Errorf("reading public area from handle 0x%x: %w", handle, err)
	}

	// Validate the key properties against the LDevID template
	publicArea, err := readPublicRsp.OutPublic.Contents()
	if err != nil {
		return fmt.Errorf("getting public area contents: %w", err)
	}

	return t.validateKeyMatchesTemplate(publicArea, handle)
}

func (t *TPM) validateKeyMatchesTemplate(publicArea *tpm2.TPMTPublic, handle tpm2.TPMHandle) error {
	template := LDevIDTemplate

	if publicArea.Type != template.Type {
		return fmt.Errorf("key at handle 0x%x type %v does not match template type %v", handle, publicArea.Type, template.Type)
	}

	if publicArea.NameAlg != template.NameAlg {
		return fmt.Errorf("key at handle 0x%x name algorithm %v does not match template %v", handle, publicArea.NameAlg, template.NameAlg)
	}

	attrs := publicArea.ObjectAttributes
	templateAttrs := template.ObjectAttributes

	if attrs.FixedTPM != templateAttrs.FixedTPM {
		return fmt.Errorf("key at handle 0x%x FixedTPM attribute %v does not match template %v", handle, attrs.FixedTPM, templateAttrs.FixedTPM)
	}

	if attrs.SignEncrypt != templateAttrs.SignEncrypt {
		return fmt.Errorf("key at handle 0x%x SignEncrypt attribute %v does not match template %v", handle, attrs.SignEncrypt, templateAttrs.SignEncrypt)
	}

	if attrs.SensitiveDataOrigin != templateAttrs.SensitiveDataOrigin {
		return fmt.Errorf("key at handle 0x%x SensitiveDataOrigin attribute %v does not match template %v", handle, attrs.SensitiveDataOrigin, templateAttrs.SensitiveDataOrigin)
	}

	return nil
}

func (t *TPM) saveLDevIDBlob(public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate, path string, readWriter fileio.ReadWriter) error {
	blob := ldevIDBlob{
		PublicBlob:  public.Bytes(),
		PrivateBlob: private.Buffer,
	}

	data, err := yaml.Marshal(blob)
	if err != nil {
		return fmt.Errorf("marshaling blob to YAML: %w", err)
	}

	err = readWriter.WriteFile(path, data, 0600)
	if err != nil {
		return fmt.Errorf("writing blob to file %s: %v", path, err)
	}

	return nil
}

func (t *TPM) loadLDevIDBlob(path string, readWriter fileio.ReadWriter) (tpm2.TPM2BPublic, tpm2.TPM2BPrivate, error) {
	var public tpm2.TPM2BPublic
	var private tpm2.TPM2BPrivate

	data, err := readWriter.ReadFile(path)
	if err != nil {
		return public, private, err
	}

	var blob ldevIDBlob
	err = yaml.Unmarshal(data, &blob)
	if err != nil {
		return public, private, fmt.Errorf("unmarshaling YAML from file %s: %v", path, err)
	}

	public = tpm2.BytesAs2B[tpm2.TPMTPublic](blob.PublicBlob)
	private.Buffer = blob.PrivateBlob

	return public, private, nil
}

// EnsureLDevID ensures an LDevID key exists using the specified storage strategy.
// If no option is provided, a transient key is created by default.
// The Storage Root Key (srk) is used as the parent for the LDevID.
func (t *TPM) EnsureLDevID(srk tpm2.NamedHandle, opt EnsureLDevIDOption) (*tpm2.NamedHandle, error) {
	var options ensureLDevIDOptions
	if opt == nil {
		opt = WithTransientKey()
	}
	opt(&options)
	return options.ensureStrategy.execute(t, srk)
}

func (t *TPM) ensureLDevIDPersistent(srk tpm2.NamedHandle, handle tpm2.TPMHandle) (*tpm2.NamedHandle, error) {
	ldevid, err := t.loadLDevIDFromPersistentHandle(handle)
	if err == nil {
		err = t.validateLDevIDKey(handle, &srk)
		if err != nil {
			return nil, fmt.Errorf("validating existing key at handle 0x%x: %w", handle, err)
		}
		return ldevid, nil
	}

	ldevid, err = t.CreateLDevID(srk)
	if err != nil {
		return nil, fmt.Errorf("creating new LDevID: %w", err)
	}

	err = t.persistLDevID(ldevid, handle)
	if err != nil {
		return nil, fmt.Errorf("persisting LDevID to handle 0x%x: %w", handle, err)
	}

	// Flush the transient handle context before loading the persistent handle
	err = t.flushContext()
	if err != nil {
		return nil, fmt.Errorf("flushing context after persistence: %w", err)
	}

	return t.loadLDevIDFromPersistentHandle(handle)
}

func (t *TPM) ensureLDevIDBlob(srk tpm2.NamedHandle, path string, readWriter fileio.ReadWriter) (*tpm2.NamedHandle, error) {
	// Try to load existing blob from file
	public, private, err := t.loadLDevIDBlob(path, readWriter)
	if err == nil {
		return t.loadLDevIDFromBlob(public, private)
	}

	// If file doesn't exist, create new key and persist it
	if os.IsNotExist(err) {
		createCmd := tpm2.Create{
			ParentHandle: srk,
			InPublic:     tpm2.New2B(LDevIDTemplate),
		}
		transportTPM := transport.FromReadWriter(t.channel)
		createRsp, err := createCmd.Execute(transportTPM)
		if err != nil {
			return nil, fmt.Errorf("creating LDevID key: %w", err)
		}

		err = t.saveLDevIDBlob(createRsp.OutPublic, createRsp.OutPrivate, path, readWriter)
		if err != nil {
			return nil, fmt.Errorf("saving blob to file: %w", err)
		}

		return t.loadLDevIDFromBlob(createRsp.OutPublic, createRsp.OutPrivate)
	}

	// File exists but couldn't be loaded (corrupted, invalid format, etc.)
	return nil, fmt.Errorf("loading blob from file %s: %w", path, err)
}

func (t *TPM) readPersistentHandleFromFile(path string, readWriter fileio.ReadWriter) (tpm2.TPMHandle, error) {
	data, err := readWriter.ReadFile(path)
	if err != nil {
		return 0, err
	}

	content := string(bytes.TrimSpace(data))
	if len(content) == 0 {
		return 0, ErrEmptyFile
	}

	// Parse as hex string
	var handle uint32
	_, err = fmt.Sscanf(content, "0x%x", &handle)
	if err != nil {
		// Try parsing as decimal
		_, err = fmt.Sscanf(content, "%d", &handle)
		if err != nil {
			return 0, fmt.Errorf("invalid handle format in file %s: %s", path, content)
		}
	}

	tpmHandle := tpm2.TPMHandle(handle)
	if err := validatePersistentHandle(tpmHandle); err != nil {
		return 0, fmt.Errorf("handle from file %s: %w", path, err)
	}

	return tpmHandle, nil
}

func (t *TPM) writePersistentHandleToFile(handle tpm2.TPMHandle, path string, readWriter fileio.ReadWriter) error {
	content := fmt.Sprintf("0x%x\n", uint32(handle))
	err := readWriter.WriteFile(path, []byte(content), 0600)
	if err != nil {
		return fmt.Errorf("writing handle to file %s: %w", path, err)
	}
	return nil
}

func (t *TPM) findAvailablePersistentHandle() (tpm2.TPMHandle, error) {
	usedHandles, err := t.getPersistentHandles()
	if err != nil {
		return 0, fmt.Errorf("getting persistent handles: %w", err)
	}

	for handle := PersistentHandleMin; handle <= PersistentHandleMax; handle++ {
		if _, exists := usedHandles[handle]; !exists {
			return handle, nil
		}
	}
	return 0, fmt.Errorf("no available persistent handles found")
}

func (t *TPM) getPersistentHandles() (map[tpm2.TPMHandle]struct{}, error) {
	transportTPM := transport.FromReadWriter(t.channel)

	getCapCmd := tpm2.GetCapability{
		Capability:    tpm2.TPMCapHandles,
		Property:      uint32(PersistentHandleMin),
		PropertyCount: 1000,
	}

	usedHandles := make(map[tpm2.TPMHandle]struct{})
	for {
		capRsp, err := getCapCmd.Execute(transportTPM)
		if err != nil {
			return nil, fmt.Errorf("querying persistent handles: %w", err)
		}

		handles, err := capRsp.CapabilityData.Data.Handles()
		if err != nil {
			return nil, fmt.Errorf("parsing handles: %w", err)
		}

		if len(handles.Handle) == 0 {
			break
		}

		for _, handle := range handles.Handle {
			if handle >= PersistentHandleMin && handle <= PersistentHandleMax {
				usedHandles[handle] = struct{}{}
			}
		}

		if len(handles.Handle) < 1000 {
			break
		}

		getCapCmd.Property = uint32(handles.Handle[len(handles.Handle)-1]) + 1
	}

	return usedHandles, nil
}

func (t *TPM) ensureLDevIDPersistentPath(srk tpm2.NamedHandle, path string, readWriter fileio.ReadWriter) (*tpm2.NamedHandle, error) {
	handle, err := t.readPersistentHandleFromFile(path, readWriter)
	if err == nil {
		ldevid, err := t.loadLDevIDFromPersistentHandle(handle)
		if err == nil {
			err = t.validateLDevIDKey(handle, &srk)
			if err != nil {
				return nil, fmt.Errorf("validating existing key at handle 0x%x: %w", handle, err)
			}
			return ldevid, nil
		}
	}

	if os.IsNotExist(err) || err == ErrEmptyFile {
		ldevid, err := t.CreateLDevID(srk)
		if err != nil {
			return nil, fmt.Errorf("creating new LDevID: %w", err)
		}

		availableHandle, err := t.findAvailablePersistentHandle()
		if err != nil {
			return nil, fmt.Errorf("finding available persistent handle: %w", err)
		}

		err = t.persistLDevID(ldevid, availableHandle)
		if err != nil {
			return nil, fmt.Errorf("persisting LDevID to handle 0x%x: %w", availableHandle, err)
		}

		// Flush the transient handle context before loading the persistent handle
		err = t.flushContext()
		if err != nil {
			return nil, fmt.Errorf("flushing context after persistence: %w", err)
		}

		err = t.writePersistentHandleToFile(availableHandle, path, readWriter)
		if err != nil {
			return nil, fmt.Errorf("writing handle to file: %w", err)
		}

		return t.loadLDevIDFromPersistentHandle(availableHandle)
	}

	return nil, fmt.Errorf("loading handle from file %s: %w", path, err)
}

// Strategy implementations

func (s *persistentHandleStrategy) execute(t *TPM, srk tpm2.NamedHandle) (*tpm2.NamedHandle, error) {
	if err := validatePersistentHandle(s.handle); err != nil {
		return nil, err
	}
	return t.ensureLDevIDPersistent(srk, s.handle)
}

func (s *blobStorageStrategy) execute(t *TPM, srk tpm2.NamedHandle) (*tpm2.NamedHandle, error) {
	if s.path == "" {
		return nil, fmt.Errorf("blob path cannot be empty")
	}
	if s.readWriter == nil {
		return nil, fmt.Errorf("readWriter cannot be nil when using blob storage")
	}
	return t.ensureLDevIDBlob(srk, s.path, s.readWriter)
}

func (s *persistentPathStrategy) execute(t *TPM, srk tpm2.NamedHandle) (*tpm2.NamedHandle, error) {
	if s.path == "" {
		return nil, fmt.Errorf("persistent handle path cannot be empty")
	}
	if s.readWriter == nil {
		return nil, fmt.Errorf("readWriter cannot be nil when using persistent handle path")
	}
	return t.ensureLDevIDPersistentPath(srk, s.path, s.readWriter)
}

func (s *transientStrategy) execute(t *TPM, srk tpm2.NamedHandle) (*tpm2.NamedHandle, error) {
	return t.CreateLDevID(srk)
}
