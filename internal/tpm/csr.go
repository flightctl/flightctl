package tpm

import (
	"bytes"
	"crypto"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// TCG-CSR-IDEVID implementation according to TCG TPM 2.0 Keys for Device Identity and Attestation v1.0 Rev 12
// Section 13.1: TCG-CSR Structures. TCGCSRIDevID represents the complete TCG-CSR-IDEVID structure
// The TCG-CSR-IDEVID uses Big Endian byte ordering. All sizes are in bytes.
type TCGCSRIDevID struct {
	// Version 1.0 = 0x01000100
	StructVer [4]byte `json:"-"`
	// Size of csrContents
	Contents [4]byte `json:"-"`
	// Size, in bytes, of signature
	SigSz [4]byte `json:"-"`
	// The actual content
	CSRContents IDevIDContent `json:"csrContents"`
	// DER encoded signature, including algorithm ID
	Signature []byte `json:"signature"`
}

// MarshalJSON implements custom JSON marshaling for TCGCSRIDevID
func (t TCGCSRIDevID) MarshalJSON() ([]byte, error) {
	type serializable struct {
		StructVer   string        `json:"structVer"`
		Contents    string        `json:"contents"`
		SigSz       string        `json:"sigSz"`
		CSRContents IDevIDContent `json:"csrContents"`
		Signature   string        `json:"signature"`
	}

	s := serializable{
		StructVer:   base64.StdEncoding.EncodeToString(t.StructVer[:]),
		Contents:    base64.StdEncoding.EncodeToString(t.Contents[:]),
		SigSz:       base64.StdEncoding.EncodeToString(t.SigSz[:]),
		CSRContents: t.CSRContents,
		Signature:   base64.StdEncoding.EncodeToString(t.Signature),
	}

	return json.Marshal(s)
}

// UnmarshalJSON implements custom JSON unmarshaling for TCGCSRIDevID
func (t *TCGCSRIDevID) UnmarshalJSON(data []byte) error {
	type serializable struct {
		StructVer   string        `json:"structVer"`
		Contents    string        `json:"contents"`
		SigSz       string        `json:"sigSz"`
		CSRContents IDevIDContent `json:"csrContents"`
		Signature   string        `json:"signature"`
	}

	var s serializable
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	decode4Bytes := func(encoded string, dest *[4]byte) error {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		if len(decoded) != 4 {
			return fmt.Errorf("expected 4 bytes, got %d", len(decoded))
		}
		copy(dest[:], decoded)
		return nil
	}

	if err := decode4Bytes(s.StructVer, &t.StructVer); err != nil {
		return fmt.Errorf("decoding StructVer: %w", err)
	}
	if err := decode4Bytes(s.Contents, &t.Contents); err != nil {
		return fmt.Errorf("decoding Contents: %w", err)
	}
	if err := decode4Bytes(s.SigSz, &t.SigSz); err != nil {
		return fmt.Errorf("decoding SigSz: %w", err)
	}

	t.CSRContents = s.CSRContents

	signature, err := base64.StdEncoding.DecodeString(s.Signature)
	if err != nil {
		return fmt.Errorf("decoding Signature: %w", err)
	}
	t.Signature = signature

	return nil
}

// IDevIDContent represents the content portion of TCG-CSR-IDEVID
type IDevIDContent struct {
	StructVer  [4]byte `json:"-"` // Version 1.0 = 0x00000100
	HashAlgoId [4]byte `json:"-"` // TCG algorithm identifier for CSR hash
	HashSz     [4]byte `json:"-"` // Size, in bytes, of hash used

	// Hash of all that follows is placed here order must not change
	ProdModelSz               [4]byte `json:"-"` // Size of unterminated product model string
	ProdSerialSz              [4]byte `json:"-"` // Size of unterminated product serial number string
	ProdCaDataSz              [4]byte `json:"-"` // Size of CA-specific required data structure
	BootEvntLogSz             [4]byte `json:"-"` // Size of boot event log
	EkCertSz                  [4]byte `json:"-"` // TPM EK cert size
	AttestPubSz               [4]byte `json:"-"` // Attestation key public size
	AtCreateTktSz             [4]byte `json:"-"` // TPM2_CertifyCreation ticket size
	AtCertifyInfoSz           [4]byte `json:"-"` // TPM2_Certify info size
	AtCertifyInfoSignatureSz  [4]byte `json:"-"` // TPM2_CertifyInfo Signature size
	SigningPubSz              [4]byte `json:"-"` // Signing key public size
	SgnCertifyInfoSz          [4]byte `json:"-"` // TPM2_Certify info size
	SgnCertifyInfoSignatureSz [4]byte `json:"-"` // TPM2_CertifyInfo Signature size

	PadSz [4]byte `json:"-"` // Padding size
}

type serializable struct {
	StructVer                 string `json:"structVer"`
	HashAlgoId                string `json:"hashAlgoId"`
	HashSz                    string `json:"hashSz"`
	ProdModelSz               string `json:"prodModelSz"`
	ProdSerialSz              string `json:"prodSerialSz"`
	ProdCaDataSz              string `json:"prodCaDataSz"`
	BootEvntLogSz             string `json:"bootEvntLogSz"`
	EkCertSz                  string `json:"ekCertSz"`
	AttestPubSz               string `json:"attestPubSz"`
	AtCreateTktSz             string `json:"atCreateTktSz"`
	AtCertifyInfoSz           string `json:"atCertifyInfoSz"`
	AtCertifyInfoSignatureSz  string `json:"atCertifyInfoSignatureSz"`
	SigningPubSz              string `json:"signingPubSz"`
	SgnCertifyInfoSz          string `json:"sgnCertifyInfoSz"`
	SgnCertifyInfoSignatureSz string `json:"sgnCertifyInfoSignatureSz"`
	PadSz                     string `json:"padSz"`
}

// MarshalJSON implements custom JSON marshaling for DevIDContent
func (t IDevIDContent) MarshalJSON() ([]byte, error) {
	s := serializable{
		StructVer:                 base64.StdEncoding.EncodeToString(t.StructVer[:]),
		HashAlgoId:                base64.StdEncoding.EncodeToString(t.HashAlgoId[:]),
		HashSz:                    base64.StdEncoding.EncodeToString(t.HashSz[:]),
		ProdModelSz:               base64.StdEncoding.EncodeToString(t.ProdModelSz[:]),
		ProdSerialSz:              base64.StdEncoding.EncodeToString(t.ProdSerialSz[:]),
		ProdCaDataSz:              base64.StdEncoding.EncodeToString(t.ProdCaDataSz[:]),
		BootEvntLogSz:             base64.StdEncoding.EncodeToString(t.BootEvntLogSz[:]),
		EkCertSz:                  base64.StdEncoding.EncodeToString(t.EkCertSz[:]),
		AttestPubSz:               base64.StdEncoding.EncodeToString(t.AttestPubSz[:]),
		AtCreateTktSz:             base64.StdEncoding.EncodeToString(t.AtCreateTktSz[:]),
		AtCertifyInfoSz:           base64.StdEncoding.EncodeToString(t.AtCertifyInfoSz[:]),
		AtCertifyInfoSignatureSz:  base64.StdEncoding.EncodeToString(t.AtCertifyInfoSignatureSz[:]),
		SigningPubSz:              base64.StdEncoding.EncodeToString(t.SigningPubSz[:]),
		SgnCertifyInfoSz:          base64.StdEncoding.EncodeToString(t.SgnCertifyInfoSz[:]),
		SgnCertifyInfoSignatureSz: base64.StdEncoding.EncodeToString(t.SgnCertifyInfoSignatureSz[:]),
		PadSz:                     base64.StdEncoding.EncodeToString(t.PadSz[:]),
	}

	return json.Marshal(s)
}

// UnmarshalJSON implements custom JSON unmarshaling for TCGIDevIDContent
func (t *IDevIDContent) UnmarshalJSON(data []byte) error {
	var s serializable
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	decode := func(encoded string, dest *[4]byte) error {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		if len(decoded) != 4 {
			return fmt.Errorf("expected 4 bytes, got %d", len(decoded))
		}
		copy(dest[:], decoded)
		return nil
	}

	if err := decode(s.StructVer, &t.StructVer); err != nil {
		return fmt.Errorf("decoding StructVer: %w", err)
	}
	if err := decode(s.HashAlgoId, &t.HashAlgoId); err != nil {
		return fmt.Errorf("decoding HashAlgoId: %w", err)
	}
	if err := decode(s.HashSz, &t.HashSz); err != nil {
		return fmt.Errorf("decoding HashSz: %w", err)
	}
	if err := decode(s.ProdModelSz, &t.ProdModelSz); err != nil {
		return fmt.Errorf("decoding ProdModelSz: %w", err)
	}
	if err := decode(s.ProdSerialSz, &t.ProdSerialSz); err != nil {
		return fmt.Errorf("decoding ProdSerialSz: %w", err)
	}
	if err := decode(s.ProdCaDataSz, &t.ProdCaDataSz); err != nil {
		return fmt.Errorf("decoding ProdCaDataSz: %w", err)
	}
	if err := decode(s.BootEvntLogSz, &t.BootEvntLogSz); err != nil {
		return fmt.Errorf("decoding BootEvntLogSz: %w", err)
	}
	if err := decode(s.EkCertSz, &t.EkCertSz); err != nil {
		return fmt.Errorf("decoding EkCertSz: %w", err)
	}
	if err := decode(s.AttestPubSz, &t.AttestPubSz); err != nil {
		return fmt.Errorf("decoding AttestPubSz: %w", err)
	}
	if err := decode(s.AtCreateTktSz, &t.AtCreateTktSz); err != nil {
		return fmt.Errorf("decoding AtCreateTktSz: %w", err)
	}
	if err := decode(s.AtCertifyInfoSz, &t.AtCertifyInfoSz); err != nil {
		return fmt.Errorf("decoding AtCertifyInfoSz: %w", err)
	}
	if err := decode(s.AtCertifyInfoSignatureSz, &t.AtCertifyInfoSignatureSz); err != nil {
		return fmt.Errorf("decoding AtCertifyInfoSignatureSz: %w", err)
	}
	if err := decode(s.SigningPubSz, &t.SigningPubSz); err != nil {
		return fmt.Errorf("decoding SigningPubSz: %w", err)
	}
	if err := decode(s.SgnCertifyInfoSz, &t.SgnCertifyInfoSz); err != nil {
		return fmt.Errorf("decoding SgnCertifyInfoSz: %w", err)
	}
	if err := decode(s.SgnCertifyInfoSignatureSz, &t.SgnCertifyInfoSignatureSz); err != nil {
		return fmt.Errorf("decoding SgnCertifyInfoSignatureSz: %w", err)
	}
	if err := decode(s.PadSz, &t.PadSz); err != nil {
		return fmt.Errorf("decoding PadSz: %w", err)
	}

	return nil
}

// CSRPayload contains the actual payload data referenced by the content structure
type CSRPayload struct {
	// Product model string
	ProdModel []byte `json:"prodModel"`
	// Product serial number string
	ProdSerial []byte `json:"prodSerial"`
	// CA-specific data
	ProdCaData []byte `json:"prodCaData"`
	// Boot event log
	BootEvntLog []byte `json:"bootEvntLog"`
	// TPM EK certificate (DER format)
	EkCert []byte `json:"ekCert"`
	// Attestation key public area
	AttestPub []byte `json:"attestPub"`
	// TPM2_CertifyCreation ticket
	AtCreateTkt []byte `json:"atCreateTkt"`
	// TPM2_Certify info for attestation key (currently unused)
	AtCertifyInfo []byte `json:"atCertifyInfo"`
	// Signature over attestation certify info (currently unused)
	AtCertifyInfoSignature []byte `json:"atCertifyInfoSignature"`
	// Signing key public area
	SigningPub []byte `json:"signingPub"`
	// TPM2_Certify info for signing key
	SgnCertifyInfo []byte `json:"sgnCertifyInfo"`
	// Signature over signing certify info
	SgnCertifyInfoSignature []byte `json:"sgnCertifyInfoSignature"`
	// Padding
	Pad []byte `json:"pad"`
}

// TCG Algorithm IDs (from TCG Algorithm Registry)
const (
	TCGAlgSHA256 = 0x000B
	TCGAlgSHA384 = 0x000C
	TCGAlgSHA512 = 0x000D
)

// CSR Extension OID for TCG-CSR-IDEVID
// Using id-pkcs9-at-challengePassword temporarily - should be replaced with proper TCG OID
var TCGCSRExtensionOID = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 7}

// BuildTCGCSRIDevID creates a TCG-CSR-IDEVID structure with embedded TPM attestation data
func BuildTCGCSRIDevID(
	standardCSR []byte,
	productModel string,
	productSerial string,
	ekCert []byte,
	attestationPub []byte,
	signingPub []byte,
	signingCertifyInfo []byte,
	signingCertifySignature []byte,
	signer crypto.Signer,
) ([]byte, error) {
	// Validate signer
	if signer == nil {
		return nil, fmt.Errorf("signer cannot be nil")
	}

	// Define maximum sizes to prevent overflow (reasonable limits for TCG CSR)
	const (
		maxFieldSize  = 1 << 20 // 1MB per field
		maxTotalSize  = 1 << 24 // 16MB total
		maxStringSize = 1 << 16 // 64KB for strings
	)

	// Validate input sizes to prevent overflow
	if len(standardCSR) > maxFieldSize {
		return nil, fmt.Errorf("standardCSR too large: %d bytes (max %d)", len(standardCSR), maxFieldSize)
	}
	if len(productModel) > maxStringSize {
		return nil, fmt.Errorf("productModel too long: %d bytes (max %d)", len(productModel), maxStringSize)
	}
	if len(productSerial) > maxStringSize {
		return nil, fmt.Errorf("productSerial too long: %d bytes (max %d)", len(productSerial), maxStringSize)
	}
	if len(ekCert) > maxFieldSize {
		return nil, fmt.Errorf("ekCert too large: %d bytes (max %d)", len(ekCert), maxFieldSize)
	}
	if len(attestationPub) > maxFieldSize {
		return nil, fmt.Errorf("attestationPub too large: %d bytes (max %d)", len(attestationPub), maxFieldSize)
	}
	if len(signingPub) > maxFieldSize {
		return nil, fmt.Errorf("signingPub too large: %d bytes (max %d)", len(signingPub), maxFieldSize)
	}
	if len(signingCertifyInfo) > maxFieldSize {
		return nil, fmt.Errorf("signingCertifyInfo too large: %d bytes (max %d)", len(signingCertifyInfo), maxFieldSize)
	}
	if len(signingCertifySignature) > maxFieldSize {
		return nil, fmt.Errorf("signingCertifySignature too large: %d bytes (max %d)", len(signingCertifySignature), maxFieldSize)
	}
	// Build payload
	payload := CSRPayload{
		ProdModel:               []byte(productModel),
		ProdSerial:              []byte(productSerial),
		ProdCaData:              standardCSR, // Store the standard X.509 CSR here
		BootEvntLog:             []byte{},    // Empty for now
		EkCert:                  ekCert,
		AttestPub:               attestationPub,
		AtCreateTkt:             []byte{}, // Empty for now
		SigningPub:              signingPub,
		SgnCertifyInfo:          signingCertifyInfo,
		SgnCertifyInfoSignature: signingCertifySignature,
		Pad:                     []byte{}, // No padding needed
	}

	// Validate total payload size
	if err := validatePayloadSize(&payload, maxTotalSize); err != nil {
		return nil, err
	}

	// Build content structure with sizes
	content := IDevIDContent{
		StructVer:                 [4]byte{0x00, 0x00, 0x01, 0x00},         // Version 1.0
		HashAlgoId:                [4]byte{0x00, 0x00, 0x00, TCGAlgSHA256}, // SHA256
		HashSz:                    [4]byte{0x00, 0x00, 0x00, 32},           // SHA256 hash size
		ProdModelSz:               uint32ToBytes(safeIntToUint32(len(payload.ProdModel))),
		ProdSerialSz:              uint32ToBytes(safeIntToUint32(len(payload.ProdSerial))),
		ProdCaDataSz:              uint32ToBytes(safeIntToUint32(len(payload.ProdCaData))),
		BootEvntLogSz:             uint32ToBytes(safeIntToUint32(len(payload.BootEvntLog))),
		EkCertSz:                  uint32ToBytes(safeIntToUint32(len(payload.EkCert))),
		AttestPubSz:               uint32ToBytes(safeIntToUint32(len(payload.AttestPub))),
		AtCreateTktSz:             uint32ToBytes(safeIntToUint32(len(payload.AtCreateTkt))),
		AtCertifyInfoSz:           uint32ToBytes(safeIntToUint32(len(payload.AtCertifyInfo))),
		AtCertifyInfoSignatureSz:  uint32ToBytes(safeIntToUint32(len(payload.AtCertifyInfoSignature))),
		SigningPubSz:              uint32ToBytes(safeIntToUint32(len(payload.SigningPub))),
		SgnCertifyInfoSz:          uint32ToBytes(safeIntToUint32(len(payload.SgnCertifyInfo))),
		SgnCertifyInfoSignatureSz: uint32ToBytes(safeIntToUint32(len(payload.SgnCertifyInfoSignature))),
		PadSz:                     uint32ToBytes(safeIntToUint32(len(payload.Pad))),
	}

	// Serialize content and payload for hashing
	contentBytes, err := serializeTCGContent(content)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize TCG content: %w", err)
	}

	payloadBytes, err := serializeTCGPayload(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize TCG payload: %w", err)
	}

	// Create data to be hashed (content + payload)
	dataToHash := append(contentBytes, payloadBytes...)

	// Hash the data using SHA256 since TPM Sign expects a digest, not raw data
	hash := crypto.SHA256.New()
	hash.Write(dataToHash)
	digest := hash.Sum(nil)

	// Sign the digest
	signature, err := signer.Sign(nil, digest, crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("failed to sign TCG-CSR data: %w", err)
	}

	// Check combined content and payload size
	combinedSize := len(contentBytes) + len(payloadBytes)
	if combinedSize > maxTotalSize {
		return nil, fmt.Errorf("combined content and payload size exceeds maximum: %d bytes (max %d)", combinedSize, maxTotalSize)
	}

	// Build final TCG-CSR-IDEVID structure
	tcgCSR := TCGCSRIDevID{
		StructVer: [4]byte{0x01, 0x00, 0x01, 0x00}, // Version 1.0
		Contents:  uint32ToBytes(safeIntToUint32(combinedSize)),
		SigSz:     uint32ToBytes(safeIntToUint32(len(signature))),
		Signature: signature,
	}

	// Serialize the complete structure
	result := &bytes.Buffer{}

	// Write header
	result.Write(tcgCSR.StructVer[:])
	result.Write(tcgCSR.Contents[:])
	result.Write(tcgCSR.SigSz[:])

	// Write content and payload
	result.Write(dataToHash)

	// Write signature
	result.Write(signature)

	return result.Bytes(), nil
}

// EmbedTCGCSRInX509 embeds TCG-CSR-IDEVID data as an extension in a standard X.509 CSR
func EmbedTCGCSRInX509(standardCSR []byte, tcgCSRData []byte) ([]byte, error) {
	// Parse the existing CSR
	// For now, return the standard CSR - this needs proper X.509 extension handling
	// TODO: Implement proper X.509 extension embedding
	return standardCSR, nil
}

// Helper functions

func uint32ToBytes(val uint32) [4]byte {
	var result [4]byte
	binary.BigEndian.PutUint32(result[:], val)
	return result
}

// safeIntToUint32 safely converts an int to uint32, panicking if the value is out of range
// This is safe to use after we've already validated the lengths are within maxFieldSize (1MB)
func safeIntToUint32(val int) uint32 {
	if val < 0 || val > int(^uint32(0)) {
		panic(fmt.Sprintf("integer overflow: %d cannot be converted to uint32", val))
	}
	return uint32(val)
}

// validatePayloadSize calculates and validates the total payload size
func validatePayloadSize(payload *CSRPayload, maxSize int) error {
	// All lengths have already been validated to be within maxFieldSize (1MB),
	// so we can safely calculate the total without overflow concerns for practical data
	totalSize := len(payload.ProdModel) + len(payload.ProdSerial) + len(payload.ProdCaData) +
		len(payload.BootEvntLog) + len(payload.EkCert) + len(payload.AttestPub) +
		len(payload.AtCreateTkt) + len(payload.AtCertifyInfo) + len(payload.AtCertifyInfoSignature) +
		len(payload.SigningPub) + len(payload.SgnCertifyInfo) + len(payload.SgnCertifyInfoSignature) +
		len(payload.Pad)

	if totalSize > maxSize {
		return fmt.Errorf("total payload size exceeds maximum: %d bytes (max %d)", totalSize, maxSize)
	}
	return nil
}

func serializeTCGContent(content IDevIDContent) ([]byte, error) {
	buf := &bytes.Buffer{}

	buf.Write(content.StructVer[:])
	buf.Write(content.HashAlgoId[:])
	buf.Write(content.HashSz[:])
	buf.Write(content.ProdModelSz[:])
	buf.Write(content.ProdSerialSz[:])
	buf.Write(content.ProdCaDataSz[:])
	buf.Write(content.BootEvntLogSz[:])
	buf.Write(content.EkCertSz[:])
	buf.Write(content.AttestPubSz[:])
	buf.Write(content.AtCreateTktSz[:])
	buf.Write(content.AtCertifyInfoSz[:])
	buf.Write(content.AtCertifyInfoSignatureSz[:])
	buf.Write(content.SigningPubSz[:])
	buf.Write(content.SgnCertifyInfoSz[:])
	buf.Write(content.SgnCertifyInfoSignatureSz[:])
	buf.Write(content.PadSz[:])

	return buf.Bytes(), nil
}

func serializeTCGPayload(payload CSRPayload) ([]byte, error) {
	buf := &bytes.Buffer{}

	buf.Write(payload.ProdModel)
	buf.Write(payload.ProdSerial)
	buf.Write(payload.ProdCaData)
	buf.Write(payload.BootEvntLog)
	buf.Write(payload.EkCert)
	buf.Write(payload.AttestPub)
	buf.Write(payload.AtCreateTkt)
	buf.Write(payload.AtCertifyInfo)
	buf.Write(payload.AtCertifyInfoSignature)
	buf.Write(payload.SigningPub)
	buf.Write(payload.SgnCertifyInfo)
	buf.Write(payload.SgnCertifyInfoSignature)
	buf.Write(payload.Pad)

	return buf.Bytes(), nil
}
