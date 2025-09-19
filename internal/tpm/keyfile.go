package tpm

import (
	"encoding/asn1"
	"encoding/pem"
	"fmt"

	"github.com/google/go-tpm/tpm2"
)

// This file was created from the TPM Key Draft found at: https://www.hansenpartnership.com/draft-bottomley-tpm2-keys.html
// which does not appear to be accepted at this point.
// The ASN.1 sequence defined there is represented as:
//  TPMKey ::= SEQUENCE {
//    type        OBJECT IDENTIFIER,
//    emptyAuth   [0] EXPLICIT BOOLEAN OPTIONAL,
//    policy      [1] EXPLICIT SEQUENCE OF TPMPolicy OPTIONAL,
//    secret      [2] EXPLICIT OCTET STRING OPTIONAL,
//    authPolicy  [3] EXPLICIT SEQUENCE OF TPMAuthPolicy OPTIONAL,
//    description [4] EXPLICIT UTF8String OPTIONAL,
//    rsaParent   [5] EXPLICIT BOOLEAN OPTIONAL,
//    parent      INTEGER,
//    pubkey      OCTET STRING,
//    privkey     OCTET STRING
//  }
//
// while other implementations: tpm2-software/tpm2-tss-engine and tpm2-software/tpm2-openssl
// define the sequence as:
//
//  TPMKey ::= SEQUENCE {
//    type        OBJECT IDENTIFIER,
//    emptyAuth   [0] EXPLICIT BOOLEAN OPTIONAL,
//    parent      INTEGER,
//    pubkey      OCTET STRING,
//    privkey     OCTET STRING
//  }
//
// The use-case supported by flightctl is identical to that of tpm2-software's and therefore, will
// only generate the second sequence. This sequence is compatible with the extended sequence

// KeyFileType represents the type of TPM2 key file to generate
type KeyFileType string

const (
	// LoadableKey for keys to be loaded with TPM2_Load
	LoadableKey KeyFileType = "loadable"
	// ImportableKey for keys to be loaded with TPM2_Import
	ImportableKey KeyFileType = "importable"
	// SealedKey for keys to be extracted with TPM2_Unseal
	SealedKey KeyFileType = "sealed"
)

// OID definitions for TPM2 key types according to Hansen Partnership specification
var (
	oidLoadableKey   = asn1.ObjectIdentifier{2, 23, 133, 10, 1, 3} // id-loadablekey
	oidImportableKey = asn1.ObjectIdentifier{2, 23, 133, 10, 1, 4} // id-importablekey
	oidSealedKey     = asn1.ObjectIdentifier{2, 23, 133, 10, 1, 5} // id-sealedkey
)

// tpmKey represents the ASN.1 structure for TPM2 key files as defined by tpm2-tools.
type tpmKey struct {
	Type      asn1.ObjectIdentifier
	EmptyAuth bool `asn1:"explicit,tag:0,optional"`
	Parent    int64
	PubKey    []byte
	PrivKey   []byte
}

type KeyFileOption func(*tpmKey)

func WithEmptyAuth() KeyFileOption {
	return func(key *tpmKey) {
		key.EmptyAuth = true
	}
}

// GenerateTPM2KeyFile generates a TPM2 key file in TSS2 private key format
func GenerateTPM2KeyFile(
	keyType KeyFileType,
	parent uint32,
	public tpm2.TPM2BPublic,
	private tpm2.TPM2BPrivate,
	opts ...KeyFileOption,
) ([]byte, error) {
	publicBlob := tpm2.Marshal(public)
	privateBlob := tpm2.Marshal(private)

	if len(publicBlob) == 0 {
		return nil, fmt.Errorf("empty public portion")
	}
	if len(privateBlob) == 0 {
		return nil, fmt.Errorf("empty private portion")
	}

	var oid asn1.ObjectIdentifier
	switch keyType {
	case LoadableKey:
		oid = oidLoadableKey
	case ImportableKey:
		oid = oidImportableKey
	case SealedKey:
		oid = oidSealedKey
	default:
		return nil, fmt.Errorf("unsupported key type: %s", keyType)
	}

	tpmKey := tpmKey{
		Type:    oid,
		Parent:  int64(parent),
		PubKey:  publicBlob,
		PrivKey: privateBlob,
	}

	for _, opt := range opts {
		opt(&tpmKey)
	}

	derBytes, err := asn1.Marshal(tpmKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal TPM key to ASN.1 DER: %w", err)
	}

	pemBlock := &pem.Block{
		Type:  "TSS2 PRIVATE KEY",
		Bytes: derBytes,
	}
	pemBytes := pem.EncodeToMemory(pemBlock)
	if pemBytes == nil {
		return nil, fmt.Errorf("failed to encode TPM key to PEM format")
	}

	return pemBytes, nil
}
