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
	// tss2PEMType defines the PEM block's type for TSS2 keyfiles
	tss2PEMType = "TSS2 PRIVATE KEY"
)

// OID definitions for TPM2 key types. The Hansen draft also defines importable and sealed keys.
// They are omitted from this implementation as flightctl will not be using them.
var (
	oidLoadableKey = asn1.ObjectIdentifier{2, 23, 133, 10, 1, 3}
)

// tpmKey represents the ASN.1 structure for TPM2 key files as defined by tpm2-tools.
type tpmKey struct {
	// Type indicates whether a key is Loadable, Importable, or Sealed
	Type asn1.ObjectIdentifier
	// EmptyAuth is an optional field that indicates whether the key does or does not have auth.
	// In the majority of cases, a Key will have auth
	EmptyAuth bool `asn1:"explicit,tag:0,optional"`
	// Parent is the Key's Parent's handle
	Parent int64
	// PubKey is the Key's public portion. For Loadable keys, this is the public portion as returned from tpm2_create
	PubKey []byte
	// PrivKey is the Key's private portion. For Loadable keys, this is the private portion as returned from tpm2_create
	PrivKey []byte
}

type KeyFileOption func(*tpmKey)

func WithEmptyAuth() KeyFileOption {
	return func(key *tpmKey) {
		key.EmptyAuth = true
	}
}

func isValidTSS2ParentHandleRange(parent tpm2.TPMHandle) bool {
	// the parent handle can either be a persistent handle (0x81XXXXXX) or it can be a permanent handle
	// (Owner, Endorsement, .etc)
	return (parent >= persistentHandleMin && parent <= persistentHandleMax) ||
		(parent >= permanentHandleMin && parent <= permanentHandleMax)
}

// GenerateTPM2KeyFile generates a TPM2 key file in TSS2 private key format
func GenerateTPM2KeyFile(
	keyType KeyFileType,
	parent tpm2.TPMHandle,
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
	if !isValidTSS2ParentHandleRange(parent) {
		return nil, fmt.Errorf("invalid parent handle: %x", parent)
	}

	var oid asn1.ObjectIdentifier
	switch keyType {
	case LoadableKey:
		oid = oidLoadableKey
	default:
		return nil, fmt.Errorf("unsupported key type: %s", keyType)
	}

	key := tpmKey{
		Type:    oid,
		Parent:  int64(parent),
		PubKey:  publicBlob,
		PrivKey: privateBlob,
	}

	for _, opt := range opts {
		opt(&key)
	}

	derBytes, err := asn1.Marshal(key)
	if err != nil {
		return nil, fmt.Errorf("marshalling TPM key to ASN.1 DER: %w", err)
	}

	pemBlock := &pem.Block{
		Type:  tss2PEMType,
		Bytes: derBytes,
	}
	pemBytes := pem.EncodeToMemory(pemBlock)
	if pemBytes == nil {
		return nil, fmt.Errorf("encoding TPM key to PEM format")
	}

	return pemBytes, nil
}
