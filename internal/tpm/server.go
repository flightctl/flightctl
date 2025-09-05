package tpm

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/google/go-tpm/tpm2"
)

// TCGCSRParser provides functionality to parse TCG-CSR-IDEVID format
type TCGCSRParser struct {
	data []byte
	pos  int
}

// ParsedTCGCSR contains the parsed TCG-CSR-IDEVID data
type ParsedTCGCSR struct {
	StructVer       uint32
	Contents        uint32
	SigSz           uint32
	CSRContents     *ParsedTCGContent
	Signature       []byte
	IsValid         bool
	ValidationError string
}

// ParsedTCGContent contains the parsed content portion
type ParsedTCGContent struct {
	StructVer                 uint32
	HashAlgoId                uint32
	HashSz                    uint32
	ProdModelSz               uint32
	ProdSerialSz              uint32
	ProdCaDataSz              uint32
	BootEvntLogSz             uint32
	EkCertSz                  uint32
	AttestPubSz               uint32
	AtCreateTktSz             uint32
	AtCertifyInfoSz           uint32
	AtCertifyInfoSignatureSz  uint32
	SigningPubSz              uint32
	SgnCertifyInfoSz          uint32
	SgnCertifyInfoSignatureSz uint32
	PadSz                     uint32
	Payload                   *ParsedTCGPayload
}

// ParsedTCGPayload contains the parsed payload data
type ParsedTCGPayload struct {
	ProdModel               []byte
	ProdSerial              []byte
	ProdCaData              []byte
	BootEvntLog             []byte
	EkCert                  []byte
	AttestPub               []byte
	AtCreateTkt             []byte
	AtCertifyInfo           []byte
	AtCertifyInfoSignature  []byte
	SigningPub              []byte
	SgnCertifyInfo          []byte
	SgnCertifyInfoSignature []byte
	Pad                     []byte
}

// IsTCGCSRFormat checks if the provided data appears to be TCG-CSR-IDEVID format
func IsTCGCSRFormat(data []byte) bool {
	if len(data) < 12 {
		return false
	}

	// Check for TCG-CSR-IDEVID version signature (Version 1.0 = 0x01000100)
	parser := &TCGCSRParser{data: data, pos: 0}
	version, err := parser.readUint32()
	if err != nil {
		return false
	}

	return version == 0x01000100
}

// ParseTCGCSR parses TCG-CSR-IDEVID format data
func ParseTCGCSR(data []byte) (*ParsedTCGCSR, error) {
	// Define reasonable maximum size for TCG CSR (16MB)
	const maxTCGCSRSize = 1 << 24

	if len(data) > maxTCGCSRSize {
		return nil, fmt.Errorf("TCG CSR data too large: %d bytes (max %d)", len(data), maxTCGCSRSize)
	}

	if !IsTCGCSRFormat(data) {
		return nil, fmt.Errorf("data is not in TCG-CSR-IDEVID format")
	}

	parser := &TCGCSRParser{data: data, pos: 0}
	return parser.parse()
}

// parse performs the actual parsing
func (p *TCGCSRParser) parse() (*ParsedTCGCSR, error) {
	result := &ParsedTCGCSR{}

	// Parse header
	var err error
	result.StructVer, err = p.readUint32()
	if err != nil {
		return nil, fmt.Errorf("failed to read struct version: %w", err)
	}

	result.Contents, err = p.readUint32()
	if err != nil {
		return nil, fmt.Errorf("failed to read contents size: %w", err)
	}

	result.SigSz, err = p.readUint32()
	if err != nil {
		return nil, fmt.Errorf("failed to read signature size: %w", err)
	}

	// Parse content
	result.CSRContents, err = p.parseContent()
	if err != nil {
		return nil, fmt.Errorf("failed to parse CSR contents: %w", err)
	}

	// Parse signature
	result.Signature, err = p.readBytes(int(result.SigSz))
	if err != nil {
		return nil, fmt.Errorf("failed to read signature: %w", err)
	}

	result.IsValid = true
	return result, nil
}

// parseContent parses the content portion
func (p *TCGCSRParser) parseContent() (*ParsedTCGContent, error) {
	content := &ParsedTCGContent{}
	var err error

	// Parse content header
	content.StructVer, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.HashAlgoId, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.HashSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	// Parse size fields
	content.ProdModelSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.ProdSerialSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.ProdCaDataSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.BootEvntLogSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.EkCertSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.AttestPubSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.AtCreateTktSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.AtCertifyInfoSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.AtCertifyInfoSignatureSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.SigningPubSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.SgnCertifyInfoSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.SgnCertifyInfoSignatureSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	content.PadSz, err = p.readUint32()
	if err != nil {
		return nil, err
	}

	// Parse payload
	content.Payload, err = p.parsePayload(content)
	if err != nil {
		return nil, err
	}

	return content, nil
}

// parsePayload parses the payload portion
func (p *TCGCSRParser) parsePayload(content *ParsedTCGContent) (*ParsedTCGPayload, error) {
	payload := &ParsedTCGPayload{}
	var err error

	payload.ProdModel, err = p.readBytes(int(content.ProdModelSz))
	if err != nil {
		return nil, err
	}

	payload.ProdSerial, err = p.readBytes(int(content.ProdSerialSz))
	if err != nil {
		return nil, err
	}

	payload.ProdCaData, err = p.readBytes(int(content.ProdCaDataSz))
	if err != nil {
		return nil, err
	}

	payload.BootEvntLog, err = p.readBytes(int(content.BootEvntLogSz))
	if err != nil {
		return nil, err
	}

	payload.EkCert, err = p.readBytes(int(content.EkCertSz))
	if err != nil {
		return nil, err
	}

	payload.AttestPub, err = p.readBytes(int(content.AttestPubSz))
	if err != nil {
		return nil, err
	}

	payload.AtCreateTkt, err = p.readBytes(int(content.AtCreateTktSz))
	if err != nil {
		return nil, err
	}

	payload.AtCertifyInfo, err = p.readBytes(int(content.AtCertifyInfoSz))
	if err != nil {
		return nil, err
	}

	payload.AtCertifyInfoSignature, err = p.readBytes(int(content.AtCertifyInfoSignatureSz))
	if err != nil {
		return nil, err
	}

	payload.SigningPub, err = p.readBytes(int(content.SigningPubSz))
	if err != nil {
		return nil, err
	}

	payload.SgnCertifyInfo, err = p.readBytes(int(content.SgnCertifyInfoSz))
	if err != nil {
		return nil, err
	}

	payload.SgnCertifyInfoSignature, err = p.readBytes(int(content.SgnCertifyInfoSignatureSz))
	if err != nil {
		return nil, err
	}

	payload.Pad, err = p.readBytes(int(content.PadSz))
	if err != nil {
		return nil, err
	}

	return payload, nil
}

func (p *TCGCSRParser) readUint32() (uint32, error) {
	if p.pos+4 > len(p.data) {
		return 0, fmt.Errorf("insufficient data for uint32 at position %d", p.pos)
	}

	// TCG-CSR-IDEVID spec uses Big Endian encoding
	val := binary.BigEndian.Uint32(p.data[p.pos : p.pos+4])
	p.pos += 4
	return val, nil
}

func (p *TCGCSRParser) readBytes(length int) ([]byte, error) {
	// Define reasonable maximum field size (8MB to accommodate larger boot event logs)
	const maxFieldSize = 1 << 23

	if length < 0 {
		return nil, fmt.Errorf("invalid negative length: %d", length)
	}

	if length > maxFieldSize {
		return nil, fmt.Errorf("field size too large: %d bytes (max %d)", length, maxFieldSize)
	}

	if length == 0 {
		return []byte{}, nil
	}

	if p.pos+length > len(p.data) {
		return nil, fmt.Errorf("insufficient data for %d bytes at position %d", length, p.pos)
	}

	data := make([]byte, length)
	copy(data, p.data[p.pos:p.pos+length])
	p.pos += length
	return data, nil
}

// extractTPMDataFromTCGCSR extracts TPM attestation data from parsed TCG-CSR
func extractTPMDataFromTCGCSR(parsed *ParsedTCGCSR) (*TPMAttestationData, error) {
	if parsed == nil || parsed.CSRContents == nil || parsed.CSRContents.Payload == nil {
		return nil, fmt.Errorf("invalid parsed TCG-CSR data")
	}

	payload := parsed.CSRContents.Payload

	return &TPMAttestationData{
		EKCertificate:          payload.EkCert,
		LAKPublicKey:           payload.AttestPub,
		LAKCertifyInfo:         payload.AtCertifyInfo,
		LAKCertifySignature:    payload.AtCertifyInfoSignature,
		LDevIDPublicKey:        payload.SigningPub,
		LDevIDCertifyInfo:      payload.SgnCertifyInfo,
		LDevIDCertifySignature: payload.SgnCertifyInfoSignature,
		ProductModel:           string(payload.ProdModel),
		ProductSerial:          string(payload.ProdSerial),
		StandardCSR:            payload.ProdCaData, // Extract the embedded X.509 CSR
	}, nil
}

// TPMAttestationData represents the extracted TPM data in a usable format
type TPMAttestationData struct {
	EKCertificate          []byte
	LAKPublicKey           []byte
	LAKCertifyInfo         []byte // (currently unused)
	LAKCertifySignature    []byte // (currently unused)
	LDevIDPublicKey        []byte
	LDevIDCertifyInfo      []byte
	LDevIDCertifySignature []byte
	ProductModel           string
	ProductSerial          string
	StandardCSR            []byte // Embedded standard X.509 CSR if available
}

// VerifyTCGCSRChainOfTrust verifies the complete chain of trust in a TCG-CSR-IDEVID
func VerifyTCGCSRChainOfTrust(csrData []byte) error {
	return VerifyTCGCSRChainOfTrustWithRoots(csrData, nil)
}

// VerifyTCGCSRChainOfTrustWithRoots verifies the complete chain of trust in a TCG-CSR-IDEVID
// including validation against trusted root CAs
func VerifyTCGCSRChainOfTrustWithRoots(csrData []byte, trustedRoots *x509.CertPool) error {
	// Parse the TCG-CSR-IDEVID
	parsed, err := ParseTCGCSR(csrData)
	if err != nil {
		return fmt.Errorf("failed to parse TCG-CSR: %w", err)
	}

	if !parsed.IsValid {
		return fmt.Errorf("invalid TCG-CSR: %s", parsed.ValidationError)
	}

	payload := parsed.CSRContents.Payload
	if payload == nil {
		return fmt.Errorf("missing payload in TCG-CSR")
	}

	// Extract the EK certificate
	if len(payload.EkCert) == 0 {
		return fmt.Errorf("missing EK certificate in TCG-CSR")
	}

	ekCert, err := x509.ParseCertificate(payload.EkCert)
	if err != nil {
		return fmt.Errorf("failed to parse EK certificate: %w", err)
	}

	// verify EK certificate chain against trusted roots
	if err := verifyEKCertificateChain(ekCert, trustedRoots); err != nil {
		return fmt.Errorf("EK certificate chain validation failed: %w", err)
	}

	// verify LDevID was certified by AK
	if err := verifySigningKeyChain(payload); err != nil {
		return fmt.Errorf("LDevID chain of trust verification failed: %w", err)
	}

	return nil
}

// Updated verifySigningKeyChain function for tcg_csr_parser.go
func verifySigningKeyChain(payload *ParsedTCGPayload) error {
	if len(payload.AttestPub) == 0 {
		return fmt.Errorf("missing attestation public key")
	}
	if len(payload.SigningPub) == 0 {
		return fmt.Errorf("missing signing public key")
	}
	if len(payload.SgnCertifyInfo) == 0 {
		return fmt.Errorf("missing signing certify info")
	}
	if len(payload.SgnCertifyInfoSignature) == 0 {
		return fmt.Errorf("missing signing certify signature")
	}

	// decode the TPMT_PUBLIC blob from AttestPub (LAK)
	akPub, err := tpm2.Unmarshal[tpm2.TPM2BPublic](payload.AttestPub)
	if err != nil {
		return fmt.Errorf("decoding AK public blob: %w", err)
	}

	// extract crypto.PublicKey from TPMT_PUBLIC
	akPubStruct, err := akPub.Contents()
	if err != nil {
		return fmt.Errorf("extracting crypto.PublicKey from AK TPMT_PUBLIC: %w", err)
	}

	akCryptoKey, err := tpm2.Pub(*akPubStruct)
	if err != nil {
		return fmt.Errorf("converting AK TPMTPublic to Go key: %w", err)
	}

	return verifyCertifiedKey(
		payload.SgnCertifyInfo,
		payload.SgnCertifyInfoSignature,
		payload.SigningPub,
		akCryptoKey,
	)
}

func verifyCertifiedKey(certifyInfo, signature, pubBlob []byte, signerKey crypto.PublicKey) error {
	// parse TPM2B_PUBLIC blob and compute its TPM Name
	pub, err := tpm2.Unmarshal[tpm2.TPM2BPublic](pubBlob)
	if err != nil {
		return fmt.Errorf("unmarshalling certified key blob: %w", err)
	}
	pubContents, err := pub.Contents()
	if err != nil {
		return fmt.Errorf("extracting certified key contents: %w", err)
	}

	// compute the TPM Name
	computedName, err := computeTPMName(pubContents)
	if err != nil {
		return fmt.Errorf("computing TPM Name: %w", err)
	}

	// TODO: this needs to be hardened contains is a poor compare.
	if !bytes.Contains(certifyInfo, computedName) {
		return fmt.Errorf("certified object name not found in certify info")
	}

	// verify the signature
	return verifyTPM2CertifySignature(certifyInfo, signature, signerKey)
}

func computeTPMName(pub *tpm2.TPMTPublic) ([]byte, error) {
	// marshal the TPMT_PUBLIC structure
	pubBytes := tpm2.Marshal(*pub)

	// hash the marshaled public key using the name algorithm
	var hasher crypto.Hash
	switch pub.NameAlg {
	case tpm2.TPMAlgSHA256:
		hasher = crypto.SHA256
	default:
		return nil, fmt.Errorf("unsupported NameAlg: 0x%x", pub.NameAlg)
	}

	h := hasher.New()
	h.Write(pubBytes)
	digest := h.Sum(nil)

	// TPM Name format: algorithm identifier (2 bytes) + digest
	algPrefix := make([]byte, 2)
	binary.BigEndian.PutUint16(algPrefix, uint16(pub.NameAlg))

	return append(algPrefix, digest...), nil
}

func verifyTPM2CertifySignature(certifyInfo, signature []byte, signingPublicKey crypto.PublicKey) error {
	// handle TPM2B_ATTEST format - extract just the TPMS_ATTEST part for signature verification
	var attestData []byte
	if len(certifyInfo) >= 2 {
		attestLength := binary.BigEndian.Uint16(certifyInfo[0:2])
		if int(attestLength)+2 == len(certifyInfo) {
			// This is TPM2B_ATTEST format - use only the TPMS_ATTEST part
			attestData = certifyInfo[2:]
		} else {
			// raw TPMS_ATTEST
			attestData = certifyInfo
		}
	} else {
		attestData = certifyInfo
	}

	// parse the TPM2 signature
	sig, err := tpm2.Unmarshal[tpm2.TPMTSignature](signature)
	if err != nil {
		return fmt.Errorf("unmarshalling TPMT_SIGNATURE: %w", err)
	}

	// hash the TPMS_ATTEST data
	digest := sha256.Sum256(attestData)

	// verify based on algorithm - supports both ECDSA and RSA
	switch sig.SigAlg {
	case tpm2.TPMAlgECDSA:
		sigECDSA, err := sig.Signature.ECDSA()
		if err != nil {
			return fmt.Errorf("extracting ECDSA signature: %w", err)
		}

		ecdsaKey, ok := signingPublicKey.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("signing key is not ECDSA")
		}

		r := new(big.Int).SetBytes(sigECDSA.SignatureR.Buffer)
		s := new(big.Int).SetBytes(sigECDSA.SignatureS.Buffer)

		if !ecdsa.Verify(ecdsaKey, digest[:], r, s) {
			return fmt.Errorf("ECDSA signature verify failed")
		}
		return nil

	case tpm2.TPMAlgRSASSA:
		sigRSA, err := sig.Signature.RSASSA()
		if err != nil {
			return fmt.Errorf("extracting RSASSA signature: %w", err)
		}

		rsaKey, ok := signingPublicKey.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("signing key is not RSA")
		}

		if err := rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, digest[:], sigRSA.Sig.Buffer); err != nil {
			return fmt.Errorf("RSA signature verify failed: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("unsupported TPM signature algorithm: %v", sig.SigAlg)
	}
}

// stripSANExtensionOIDs removes the SAN Extension OID from the specified
// cert. This method may re-assign the remaining extensions out of order.
//
// This is necessary because the EKCert may contain additional data
// bundled within the SAN extension. This ext is also sometimes marked
// critical. This causes the Verify() to reject the cert because not all data
// within a critical extension has been handled. We mark this as OK here by
// stripping the SAN Extension OID out of UnhandledCriticalExtensions.
func stripSANExtensionOIDs(cert *x509.Certificate) {
	sanExtensionOID := []int{2, 5, 29, 17}

	for i := 0; i < len(cert.UnhandledCriticalExtensions); i++ {
		ext := cert.UnhandledCriticalExtensions[i]
		if !ext.Equal(sanExtensionOID) {
			continue
		}
		// Swap ext with the last index and remove it.
		last := len(cert.UnhandledCriticalExtensions) - 1
		cert.UnhandledCriticalExtensions[i] = cert.UnhandledCriticalExtensions[last]
		cert.UnhandledCriticalExtensions[last] = nil // "Release" extension
		cert.UnhandledCriticalExtensions = cert.UnhandledCriticalExtensions[:last]
		i--
	}
}

// verifyEKCertificateChain verifies that the EK certificate chains to a trusted root CA
func verifyEKCertificateChain(ekCert *x509.Certificate, trustedRoots *x509.CertPool) error {
	if ekCert == nil {
		return fmt.Errorf("no EK certificate provided")
	}

	// basic certificate validity check
	now := time.Now()
	if now.Before(ekCert.NotBefore) || now.After(ekCert.NotAfter) {
		return fmt.Errorf("EK certificate is not valid at current time")
	}

	// chain validation requires trusted roots
	if trustedRoots == nil {
		return fmt.Errorf("TPM CA certificates not configured - cannot verify EK certificate chain")
	}

	// strip SAN Extension OIDs for TPM certificates
	stripSANExtensionOIDs(ekCert)

	opts := x509.VerifyOptions{
		Roots:     trustedRoots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}

	_, err := ekCert.Verify(opts)
	if err != nil {
		return fmt.Errorf("chain validation failed: %w", err)
	}

	return nil
}

// LoadCAsFromPaths loads CA certificates from a list of file paths
func LoadCAsFromPaths(paths []string) (*x509.CertPool, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	rootPool := x509.NewCertPool()
	loadedCount := 0

	for _, certPath := range paths {
		certData, err := os.ReadFile(certPath)
		if err != nil {
			continue
		}

		// Try to parse as PEM first
		block, _ := pem.Decode(certData)
		if block != nil {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				continue
			}
			rootPool.AddCert(cert)
			loadedCount++
		} else {
			// Try as DER
			cert, err := x509.ParseCertificate(certData)
			if err != nil {
				continue
			}
			rootPool.AddCert(cert)
			loadedCount++
		}
	}

	if loadedCount == 0 {
		return nil, fmt.Errorf("no valid CA certificates could be loaded from the provided paths")
	}

	return rootPool, nil
}

// ParseTCGCSRBytes returns the decoded TCG-formatted CSR bytes if valid, or false if not.
func ParseTCGCSRBytes(raw string) ([]byte, bool) {
	csrBytes := []byte(raw)

	if IsTCGCSRFormat(csrBytes) {
		return csrBytes, true
	}

	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && IsTCGCSRFormat(decoded) {
		return decoded, true
	}

	return nil, false
}

// NormalizeEnrollmentCSR extracts the embedded standard X.509 CSR from a TCG CSR if present.
func NormalizeEnrollmentCSR(csrString string) ([]byte, bool, error) {
	csrBytes, isTPM := ParseTCGCSRBytes(csrString)
	if !isTPM {
		// standard CSR - return as-is
		return []byte(csrString), false, nil
	}

	parsed, err := ParseTCGCSR(csrBytes)
	if err != nil {
		return nil, true, fmt.Errorf("failed to parse TCG CSR: %w", err)
	}

	// extract TPM data including the embedded standard CSR
	tpmData, err := extractTPMDataFromTCGCSR(parsed)
	if err != nil {
		return nil, true, fmt.Errorf("failed to extract TPM data: %w", err)
	}

	if len(tpmData.StandardCSR) == 0 {
		return nil, true, fmt.Errorf("invalid X.509 data parsed from TCG CSR")
	}

	return tpmData.StandardCSR, true, nil
}

// CredentialChallenge contains the components of a TPM credential challenge
type CredentialChallenge struct {
	CredentialBlob  []byte
	EncryptedSecret []byte
	ExpectedSecret  []byte
}

// CreateCredentialChallenge generates a challenge credential that can be solved using TPM2_ActivateCredential.
// It takes an EK certificate and any TPM public key as byte arrays and returns a CredentialChallenge
// containing the credential blob, encrypted secret, and expected secret for verification. No calls to a TPM are made.
func CreateCredentialChallenge(ekCertBytes []byte, publicKeyBytes []byte) (*CredentialChallenge, error) {
	if len(ekCertBytes) == 0 {
		return nil, fmt.Errorf("EK certificate is empty")
	}
	if len(publicKeyBytes) == 0 {
		return nil, fmt.Errorf("public key is empty")
	}

	// Convert the EK Cert into a format that can be used to create a credential
	ekCert, err := x509.ParseCertificate(ekCertBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing EK certificate: %w", err)
	}

	ekPublicKey, err := convertEKPublicKeyToTPMTPublic(ekCert.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("converting EK public key: %w", err)
	}

	encapsulationKey, err := tpm2.ImportEncapsulationKey(ekPublicKey)
	if err != nil {
		return nil, fmt.Errorf("creating encapsulation key: %w", err)
	}

	// convert the public key bytes into a name
	publicKey, err := tpm2.Unmarshal[tpm2.TPM2BPublic](publicKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling public key: %w", err)
	}

	publicKeyContents, err := publicKey.Contents()
	if err != nil {
		return nil, fmt.Errorf("public key contents: %w", err)
	}

	publicKeyName, err := computeTPMName(publicKeyContents)
	if err != nil {
		return nil, fmt.Errorf("computing public key name: %w", err)
	}

	// Generate random secret
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generating random secret: %w", err)
	}

	idObject, encSecret, err := tpm2.CreateCredential(rand.Reader, encapsulationKey, publicKeyName, secret)
	if err != nil {
		return nil, fmt.Errorf("creating credential: %w", err)
	}

	return &CredentialChallenge{
		CredentialBlob:  idObject,
		EncryptedSecret: encSecret,
		ExpectedSecret:  secret,
	}, nil
}

// convertEKPublicKeyToTPMTPublic converts the public key of an EK to TPM format
func convertEKPublicKeyToTPMTPublic(publicKey crypto.PublicKey) (*tpm2.TPMTPublic, error) {
	switch key := publicKey.(type) {
	case *ecdsa.PublicKey:
		return convertEKECDSAPublicKey(key)
	case *rsa.PublicKey:
		return convertEKRSAPublicKey(key)
	default:
		return nil, fmt.Errorf("unsupported public key type: %T", publicKey)
	}
}

// convertEKECDSAPublicKey converts an ECDSA public key to TPM format
func convertEKECDSAPublicKey(key *ecdsa.PublicKey) (*tpm2.TPMTPublic, error) {
	// Currently only RSA2048 and ECC P256 EK Certs are supported. Should support for more ECC curves be added, this will
	// need to change.
	switch key.Curve.Params().Name {
	case "P-256":
	default:
		return nil, fmt.Errorf("unsupported ECDSA curve: %s", key.Curve.Params().Name)
	}

	tpmPublic := tpm2.ECCEKTemplate

	// put actual key data into the unique portion
	tpmPublic.Unique = tpm2.NewTPMUPublicID(
		tpm2.TPMAlgECC,
		&tpm2.TPMSECCPoint{
			// 32 as defined by the P256. Should more curves be supported this will change
			X: tpm2.TPM2BECCParameter{Buffer: key.X.FillBytes(make([]byte, 32))},
			Y: tpm2.TPM2BECCParameter{Buffer: key.Y.FillBytes(make([]byte, 32))},
		},
	)

	return &tpmPublic, nil
}

// convertEKRSAPublicKey converts an RSA public key to TPM format
func convertEKRSAPublicKey(key *rsa.PublicKey) (*tpm2.TPMTPublic, error) {
	// Currently only RSA2048 and ECC P256 EK Certs are supported. Should support for more RSA Key sizes be added,
	// this will need to change
	if key.Size() != 256 {
		return nil, fmt.Errorf("unsupported RSA key size: %d bits", key.Size()*8)
	}

	tpmPublic := tpm2.RSAEKTemplate

	// Only replace the Unique field with the actual public key data
	tpmPublic.Unique = tpm2.NewTPMUPublicID(
		tpm2.TPMAlgRSA,
		&tpm2.TPM2BPublicKeyRSA{Buffer: key.N.Bytes()},
	)

	return &tpmPublic, nil
}
