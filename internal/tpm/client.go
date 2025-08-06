package tpm

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"strings"

	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpmutil"
)

// Ensure Client implements crypto.Signer interface
var _ crypto.Signer = (*Client)(nil)

// Client represents a simplified TPM client that exposes signing capabilities
// and attestation data for CSR generation.
type Client struct {
	session       Session
	log           *log.PrefixLogger
	productModel  string
	productSerial string
}

// NewClient creates a new simplified TPM client with the given configuration.
func NewClient(log *log.PrefixLogger, rw fileio.ReadWriter, config *agent_config.Config) (*Client, error) {
	sysPath := config.TPM.Path

	// discover and validate TPM device
	tpmPath, err := discoverAndValidateTPM(rw, log, sysPath)
	if err != nil {
		return nil, fmt.Errorf("discovering TPM: %w", err)
	}

	// open the TPM connection
	conn, err := tpmutil.OpenTPM(tpmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open TPM device at %s: %w", tpmPath, err)
	}

	// collect system identifiers
	productModel, productSerial := getSystemIdentifiers(log, rw)

	return newClientWithConnection(conn, log, rw, config, productModel, productSerial)
}

// newClientWithConnection creates a new TPM client with the provided connection.
// This helper function is useful for testing with simulators.
func newClientWithConnection(conn io.ReadWriteCloser, log *log.PrefixLogger, rw fileio.ReadWriter, config *agent_config.Config, productModel, productSerial string) (*Client, error) {
	// TODO: make dynamic
	keyAlgo := ECDSA

	session, err := NewSession(conn, rw, log, config.TPM.EnableOwnership, config.TPM.PersistencePath, keyAlgo)
	if err != nil {
		return nil, fmt.Errorf("creating TPM session: %w", err)
	}

	client := &Client{
		session:       session,
		log:           log,
		productModel:  productModel,
		productSerial: productSerial,
	}

	return client, nil
}

// Public returns the public key corresponding to the LDevID private key.
func (c *Client) Public() crypto.PublicKey {
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
func (c *Client) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return c.session.Sign(LDevID, digest)
}

// MakeCSR generates a TCG-CSR-IDEVID structure for enrollment requests
// This combines standard CSR data with TPM attestation according to TCG specifications
// This is the primary CSR generation method for TPM clients
func (c *Client) MakeCSR(deviceName string, qualifyingData []byte) ([]byte, error) {
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
	standardCSR, err := c.generateStandardCSR(deviceName)
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

// GetSigner returns the crypto.Signer interface for this client
func (c *Client) GetSigner() crypto.Signer {
	return c
}

// UpdateNonce updates the nonce used for TPM operations
func (c *Client) UpdateNonce(nonce []byte) error {
	// Store nonce for future use in attestation operations
	// For now, this is a no-op as the session handles nonce internally
	return nil
}

// VendorInfoCollector returns TPM vendor information for system info collection
func (c *Client) VendorInfoCollector(ctx context.Context) string {
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
func (c *Client) AttestationCollector(ctx context.Context) string {
	// Try to get LAK public key as an attestation indicator
	_, err := c.session.GetPublicKey(LAK)
	if err != nil {
		c.log.Debugf("Failed to get LAK public key for attestation info: %v", err)
		return "attestation-unavailable"
	}

	return "attestation-available"
}

// Clear clears any stored TPM data
func (c *Client) Clear() error {
	if c.session == nil {
		return nil
	}
	return c.session.Clear()
}

// Close closes the TPM session.
func (c *Client) Close(ctx context.Context) error {
	if c.session != nil {
		return c.session.Close()
	}
	return nil
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
func (c *Client) generateStandardCSR(deviceName string) ([]byte, error) {
	// Determine signature algorithm based on public key type
	var sigAlgo x509.SignatureAlgorithm
	pubKey := c.Public()
	switch pubKey.(type) {
	case *ecdsa.PublicKey:
		sigAlgo = x509.ECDSAWithSHA256
	case *rsa.PublicKey:
		sigAlgo = x509.SHA256WithRSA
	default:
		// Use ECDSAWithSHA256 as default for TPM 2.0
		sigAlgo = x509.ECDSAWithSHA256
	}

	// Create CSR template
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: deviceName,
		},
		SignatureAlgorithm: sigAlgo,
	}

	// Generate CSR using the TPM signer
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, c.GetSigner())
	if err != nil {
		return nil, fmt.Errorf("creating certificate request: %w", err)
	}

	// Encode to PEM
	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	})

	return csrPEM, nil
}
