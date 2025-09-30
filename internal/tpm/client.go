package tpm

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"strings"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpmutil"
)

// Ensure client implements crypto.Signer interface
var _ crypto.Signer = (*client)(nil)

// Ensure client implements Client interface
var _ Client = (*client)(nil)

type connFactory func() (io.ReadWriteCloser, error)

// Client represents a simplified TPM client that exposes signing capabilities
// and attestation data for CSR generation.
type client struct {
	session       Session
	connFactory   connFactory
	sessionOpts   []SessionOption
	log           *log.PrefixLogger
	rw            fileio.ReadWriter
	productModel  string
	productSerial string
}

// NewClient creates a new simplified TPM client with the given configuration.
func NewClient(log *log.PrefixLogger, rw fileio.ReadWriter, config *agent_config.Config) (Client, error) {
	devicePath := config.TPM.DevicePath

	// discover and validate TPM device
	tpmPath, err := discoverAndValidateTPM(rw, log, devicePath)
	if err != nil {
		return nil, fmt.Errorf("discovering TPM: %w", err)
	}

	// open the TPM connection
	connFact := func() (io.ReadWriteCloser, error) {
		conn, err := tpmutil.OpenTPM(tpmPath)
		if err != nil {
			return nil, fmt.Errorf("opening TPM device at %s: %w", tpmPath, err)
		}
		return conn, err
	}

	// collect system identifiers
	productModel, productSerial := getSystemIdentifiers(log, rw)

	return newClientWithConnection(connFact, log, rw, config, productModel, productSerial)
}

// newClientWithConnection creates a new TPM client with the provided connection.
// This helper function is useful for testing with simulators.
func newClientWithConnection(conFact connFactory, log *log.PrefixLogger, rw fileio.ReadWriter, config *agent_config.Config, productModel, productSerial string) (*client, error) {
	// TODO: make dynamic
	keyAlgo := ECDSA

	storage := NewFileStorage(rw, config.TPM.StorageFilePath, log)
	c := &client{
		log:         log,
		connFactory: conFact,
		sessionOpts: []SessionOption{
			WithAuth(config.TPM.AuthEnabled),
			WithKeyAlgo(keyAlgo),
			WithStorage(storage),
		},
		rw:            rw,
		productModel:  productModel,
		productSerial: productSerial,
	}

	conn, err := conFact()
	if err != nil {
		return nil, fmt.Errorf("connecting to TPM: %w", err)
	}

	opts := append([]SessionOption{WithInitialization()}, c.sessionOpts...)
	session, err := NewSession(conn, log, rw, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating TPM session: %w", err)
	}

	c.session = session

	return c, nil
}

// Public returns the public key corresponding to the LDevID private key.
func (c *client) Public() crypto.PublicKey {
	pub, err := c.session.GetPublicKey(LDevID)
	if err != nil {
		c.log.Errorf("Failed to get LDevID public key from TPM: %v", err)
		return nil
	}

	// convert TPM2BPublic to crypto.PublicKey
	pubKey, err := convertTPM2BPublicToPublicKey(pub)
	if err != nil {
		c.log.Errorf("Failed to convert TPM public key: %v", err)
		return nil
	}

	return pubKey
}

// Sign implements the crypto.Signer interface using the TPM's LDevID key.
// The rand parameter is ignored as the TPM generates its own randomness internally.
func (c *client) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return c.session.Sign(LDevID, digest)
}

// MakeCSR generates a TCG-CSR-IDEVID structure for enrollment requests
// This combines standard CSR data with TPM attestation according to TCG specifications
// This is the primary CSR generation method for TPM clients
func (c *client) MakeCSR(deviceName string, qualifyingData []byte) ([]byte, error) {
	c.log.Tracef("[MakeCSR] Starting CSR generation for device: %s", deviceName)
	defer func() { c.log.Tracef("[MakeCSR] Finished CSR generation for device: %s", deviceName) }()

	c.log.Tracef("[MakeCSR] Getting EK certificate...")
	ekCert, err := c.session.GetEndorsementKeyCert()
	if err != nil {
		c.log.Errorf("[MakeCSR] Failed to get EK certificate: %v", err)
		return nil, fmt.Errorf("getting EK certificate: %w", err)
	}
	c.log.Tracef("[MakeCSR] Got EK certificate (%d bytes)", len(ekCert))

	// get LAK public key
	c.log.Tracef("[MakeCSR] Getting LAK public key...")
	lakPublicKey, err := c.session.GetPublicKey(LAK)
	if err != nil {
		c.log.Errorf("[MakeCSR] Failed to get LAK public key: %v", err)
		return nil, fmt.Errorf("getting LAK public key: %w", err)
	}
	c.log.Tracef("[MakeCSR] Got LAK public key")

	// get LDevID public key
	c.log.Tracef("[MakeCSR] Getting LDevID public key...")
	ldevidPublicKey, err := c.session.GetPublicKey(LDevID)
	if err != nil {
		c.log.Errorf("[MakeCSR] Failed to get LDevID public key: %v", err)
		return nil, fmt.Errorf("getting LDevID public key: %w", err)
	}
	c.log.Tracef("[MakeCSR] Got LDevID public key")

	// certify LDevID with LAK
	c.log.Tracef("[MakeCSR] Certifying LDevID with LAK...")
	ldevidCertifyInfo, ldevidCertifySignature, err := c.session.CertifyKey(LDevID, qualifyingData)
	if err != nil {
		c.log.Errorf("[MakeCSR] Failed to certify LDevID: %v", err)
		return nil, fmt.Errorf("certifying LDevID: %w", err)
	}

	lakPubBlob := tpm2.Marshal(*lakPublicKey)
	ldevidPubBlob := tpm2.Marshal(*ldevidPublicKey)

	// First, generate a standard X.509 CSR using the TPM signer
	c.log.Tracef("[MakeCSR] Generating standard X.509 CSR...")
	standardCSR, err := generateStandardCSR(deviceName, c.GetSigner())
	if err != nil {
		c.log.Errorf("[MakeCSR] Failed to generate standard CSR: %v", err)
		return nil, fmt.Errorf("generating standard CSR: %w", err)
	}
	c.log.Tracef("[MakeCSR] Generated standard X.509 CSR")

	c.log.Tracef("[MakeCSR] Building TCG-CSR-IDEVID...")
	tcgCSR, err := BuildTCGCSRIDevID(
		standardCSR,
		c.productModel,
		c.productSerial,
		ekCert,
		lakPubBlob,
		ldevidPubBlob,
		ldevidCertifyInfo,
		ldevidCertifySignature,
		c.GetSigner(),
	)
	if err != nil {
		c.log.Errorf("[MakeCSR] Failed to build TCG-CSR-IDEVID: %v", err)
		return nil, fmt.Errorf("building TCG-CSR-IDEVID: %w", err)
	}
	c.log.Tracef("[MakeCSR] Built TCG-CSR-IDEVID (%d bytes)", len(tcgCSR))

	return tcgCSR, nil
}

// RemoveApplicationKey removes the associated key from storage
func (c *client) RemoveApplicationKey(appName string) error {
	conn, err := c.connFactory()
	defer func() {
		if err := conn.Close(); err != nil {
			c.log.Errorf("[RemoveApplicationKey] Failed to close connection: %v", err)
		}
	}()
	if err != nil {
		return fmt.Errorf("creating conn: %w", err)
	}
	session, err := NewSession(conn, c.log, c.rw, c.sessionOpts...)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			c.log.Errorf("[RemoveApplicationKey] Failed to close session: %v", err)
		}
	}()
	return session.RemoveApplicationKey(appName)
}

// CreateApplicationKey creates a new device identity for the specified app if one doesn't exist. If it does exist
// the existing information is simply reused. This generates as TCG CSR IDEVID bundle and a TSS2 PEM formatted file for exporting
func (c *client) CreateApplicationKey(name string) ([]byte, []byte, error) {
	conn, err := c.connFactory()
	if err != nil {
		return nil, nil, fmt.Errorf("creating conn: %w", err)
	}

	session, err := NewSession(conn, c.log, c.rw, c.sessionOpts...)
	if err != nil {
		err = fmt.Errorf("creating session: %w", err)
		if closeErr := conn.Close(); closeErr != nil {
			err = fmt.Errorf("closing conn: %w", closeErr)
		}
		return nil, nil, err
	}
	defer func() {
		if err := session.Close(); err != nil {
			c.log.Errorf("[CreateApplicationKey] Failed to close session: %v", err)
		}
	}()

	key, err := session.LoadApplicationKey(name)
	if err != nil {
		return nil, nil, fmt.Errorf("creating application key: %w", err)
	}
	defer func() {
		if err := key.Close(); err != nil {
			c.log.Errorf("[CreateApplicationKey] Failed to close key: %q: %v", name, err)
		}
	}()

	qualifyingData := make([]byte, 32)
	if _, err = rand.Read(qualifyingData); err != nil {
		return nil, nil, fmt.Errorf("creating qualifying data: %w", err)
	}

	appCertifyInfo, appCertifySig, err := session.Certify(key, qualifyingData)
	if err != nil {
		return nil, nil, fmt.Errorf("certifying application key: %w", err)
	}
	lakPublicKey, err := session.GetPublicKey(LAK)
	if err != nil {
		return nil, nil, fmt.Errorf("getting LAK public key: %w", err)
	}
	lakPubBlob := tpm2.Marshal(*lakPublicKey)

	standardCSR, err := generateStandardCSR(name, key)
	if err != nil {
		return nil, nil, fmt.Errorf("generating standard CSR: %w", err)
	}
	// The EK cert is not required for app identities as establishing the primary identity involves
	// establishing a trusted LAK
	tcgCSR, err := BuildTCGCSRIDevID(
		standardCSR,
		c.productModel,
		c.productSerial,
		nil,
		lakPubBlob,
		key.PublicBlob(),
		appCertifyInfo,
		appCertifySig,
		key,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("building TCG-CSR-IDEVID: %w", err)
	}

	exported, err := key.Export()
	if err != nil {
		return nil, nil, fmt.Errorf("exporting key: %w", err)
	}

	return tcgCSR, exported, nil
}

// GetSigner returns the crypto.Signer interface for this client
func (c *client) GetSigner() crypto.Signer {
	return c
}

// UpdateNonce updates the nonce used for TPM operations
func (c *client) UpdateNonce(nonce []byte) error {
	// Store nonce for future use in attestation operations
	// For now, this is a no-op as the session handles nonce internally
	return nil
}

// VendorInfoCollector returns TPM vendor information for system info collection
func (c *client) VendorInfoCollector(ctx context.Context) string {
	// Try to get EK certificate for vendor info
	ekCert, err := c.session.GetEndorsementKeyCert()
	if err != nil {
		c.log.Debugf("Failed to get EK certificate for vendor info: %v", err)
		return "unknown-vendor"
	}

	// For now, return a simple indicator that we have TPM vendor info
	if len(ekCert) > 0 {
		return "tpm-vendor-available"
	}
	return "tpm-vendor-unavailable"
}

// AttestationCollector returns TPM attestation information for system info collection
func (c *client) AttestationCollector(ctx context.Context) string {
	// Try to get LAK public key as an attestation indicator
	_, err := c.session.GetPublicKey(LAK)
	if err != nil {
		c.log.Debugf("Failed to get LAK public key for attestation info: %v", err)
		return "attestation-unavailable"
	}

	return "attestation-available"
}

// Clear clears any stored TPM data
func (c *client) Clear() error {
	if c.session == nil {
		return nil
	}
	return c.session.Clear()
}

// Close closes the TPM session.
func (c *client) Close(ctx context.Context) error {
	if c.session != nil {
		return c.session.Close()
	}
	return nil
}

// SolveChallenge uses TPM2_ActivateCredential to decrypt an encrypted secret
// and prove ownership of the credentials. This is used by clients to solve challenges.
func (c *client) SolveChallenge(credentialBlob, encryptedSecret []byte) ([]byte, error) {
	return c.session.SolveChallenge(credentialBlob, encryptedSecret)
}

// convertTPM2BPublicToECDSA converts a TPM2BPublic to a public key.
func convertTPM2BPublicToPublicKey(pub *tpm2.TPM2BPublic) (crypto.PublicKey, error) {
	outpub, err := pub.Contents()
	if err != nil {
		return nil, fmt.Errorf("could not get contents of TPM2BPublic: %w", err)
	}

	switch outpub.Type {
	case tpm2.TPMAlgECC:
		details, err := outpub.Parameters.ECCDetail()
		if err != nil {
			return nil, fmt.Errorf("cannot read ECC details: %w", err)
		}

		curve, err := details.CurveID.Curve()
		if err != nil {
			return nil, fmt.Errorf("could not get curve ID: %w", err)
		}

		unique, err := outpub.Unique.ECC()
		if err != nil {
			return nil, fmt.Errorf("could not get ECC unique parameters: %w", err)
		}

		return &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(unique.X.Buffer),
			Y:     new(big.Int).SetBytes(unique.Y.Buffer),
		}, nil

	case tpm2.TPMAlgRSA:
		unique, err := outpub.Unique.RSA()
		if err != nil {
			return nil, fmt.Errorf("could not get RSA unique parameters: %w", err)
		}

		rsaDetail, err := outpub.Parameters.RSADetail()
		if err != nil {
			return nil, fmt.Errorf("cannot read RSA details: %w", err)
		}

		return &rsa.PublicKey{
			N: new(big.Int).SetBytes(unique.Buffer),
			E: int(rsaDetail.Exponent),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported key type: %d", outpub.Type)
	}
}

// tpmDevice represents basic TPM device information for discovery
type tpmDevice struct {
	index           string
	path            string
	resourceMgrPath string
	versionPath     string
	sysfsPath       string
}

// discoverAndValidateTPM finds and validates a TPM device, returning the resource manager path
func discoverAndValidateTPM(rw fileio.ReadWriter, log *log.PrefixLogger, path string) (string, error) {
	if path == "" {
		log.Infof("No TPM device provided. Selecting a default device")
		return discoverDefaultTPM(rw, log)
	}
	log.Infof("Using TPM device at %s", path)
	return resolveTPMPath(rw, path)
}

// discoverDefaultTPM finds and returns the first available valid TPM 2.0
func discoverDefaultTPM(rw fileio.ReadWriter, log *log.PrefixLogger) (string, error) {
	devices, err := discoverTPMDevices(rw)
	if err != nil {
		return "", fmt.Errorf("failed to discover TPMs: %w", err)
	}

	log.Debugf("Found %d TPMs", len(devices))

	for _, device := range devices {
		log.Debugf("Trying TPM %q at %q", device.index, device.resourceMgrPath)
		if deviceExists(rw, device.resourceMgrPath) {
			log.Debugf("Device %q exists, validating version", device.index)
			if err := validateTPMVersion2(rw, device.versionPath); err == nil {
				return device.resourceMgrPath, nil
			}
			log.Debugf("Device %q validation failed: %v", device.index, err)
		} else {
			log.Debugf("Device %q does not exist", device.index)
		}
	}

	return "", fmt.Errorf("no valid TPM 2.0 devices found")
}

// resolveTPMPath returns the resource manager path for the specified TPM device
func resolveTPMPath(rw fileio.ReadWriter, path string) (string, error) {
	devices, err := discoverTPMDevices(rw)
	if err != nil {
		return "", fmt.Errorf("discovering TPM devices: %w", err)
	}

	for _, device := range devices {
		if device.path == path || device.resourceMgrPath == path {
			if err := validateTPMVersion2(rw, device.versionPath); err != nil {
				return "", fmt.Errorf("invalid TPM %q: %w", path, err)
			}
			return device.resourceMgrPath, nil
		}
	}

	return "", fmt.Errorf("TPM %q not found", path)
}

// discoverTPMDevices scans the system for available TPM devices
func discoverTPMDevices(rw fileio.ReadWriter) ([]tpmDevice, error) {
	entries, err := rw.ReadDir(sysClassPath)
	if err != nil {
		return nil, fmt.Errorf("scanning TPM devices: %w", err)
	}

	var devices []tpmDevice
	for _, entry := range entries {
		matches := tpmIndexRegex.FindStringSubmatch(entry.Name())
		if len(matches) != 2 {
			continue
		}
		index := matches[1]

		device := tpmDevice{
			index:           index,
			path:            fmt.Sprintf(tpmPathTemplate, index),
			resourceMgrPath: fmt.Sprintf(rmPathTemplate, index),
			versionPath:     fmt.Sprintf(versionPathTemplate, entry.Name()),
			sysfsPath:       fmt.Sprintf(sysFsPathTemplate, entry.Name()),
		}

		devices = append(devices, device)
	}

	return devices, nil
}

// deviceExists checks if a TPM device exists at the given path
func deviceExists(rw fileio.ReadWriter, resourceMgrPath string) bool {
	exists, err := rw.PathExists(resourceMgrPath, fileio.WithSkipContentCheck())
	return err == nil && exists
}

// validateTPMVersion2 validates that the TPM device is version 2.0
func validateTPMVersion2(rw fileio.ReadWriter, versionPath string) error {
	versionBytes, err := rw.ReadFile(versionPath)
	if err != nil {
		return fmt.Errorf("reading tpm version file: %w", err)
	}
	versionStr := string(bytes.TrimSpace(versionBytes))
	if versionStr != "2" {
		return fmt.Errorf("TPM is not version 2.0. Found version: %s", versionStr)
	}
	return nil
}

func getSystemIdentifiers(log *log.PrefixLogger, reader fileio.ReadWriter) (productModel, productSerial string) {
	// Default fallback values
	productModel = "FlightCtl-Device"
	productSerial = "unknown-serial"

	// Try to read product name from DMI
	if productName, err := readDMIFile(reader, "product_name"); err == nil && productName != "" {
		productModel = productName
	}

	// Try to read product serial from DMI
	if serialNumber, err := readDMIFile(reader, "product_serial"); err == nil && serialNumber != "" {
		productSerial = serialNumber
	}

	log.Infof("Using system identifiers for TPM client - Model: %s, Serial: %s", productModel, productSerial)
	return productModel, productSerial
}

func readDMIFile(reader fileio.ReadWriter, fileName string) (string, error) {
	dmiPath := filepath.Join("/sys/class/dmi/id", fileName)
	content, err := reader.ReadFile(dmiPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

// generateStandardCSR creates a standard X.509 CSR using the TPM's LDevID key
func generateStandardCSR(deviceName string, signer crypto.Signer) ([]byte, error) {
	csrPem, err := fccrypto.MakeCSR(signer, deviceName)
	if err != nil {
		return nil, fmt.Errorf("creating CSR: %w", err)
	}
	return csrPem, nil
}
