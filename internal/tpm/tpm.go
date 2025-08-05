package tpm

import (
	"regexp"

	legacy "github.com/google/go-tpm/legacy/tpm2"
	"github.com/google/go-tpm/tpm2"
)

const (
	MinNonceLength = 8

	tpmPathTemplate     = "/dev/tpm%s"
	rmPathTemplate      = "/dev/tpmrm%s"
	versionPathTemplate = "/sys/class/tpm/%s/tpm_version_major"
	sysClassPath        = "/sys/class/tpm"
	sysFsPathTemplate   = "/sys/class/tpm/%s"
)

// KeyType represents the type of TPM key
type KeyType string

const (
	// LDevID (Local Device Identity Key) is a unique identity key for the device,
	// used to authenticate the device to external services.
	LDevID KeyType = "ldevid"

	// LAK (Local Attestation Key) is a restricted signing key used for TPM attestation operations.
	LAK KeyType = "lak"

	// SRK (Storage Root Key) is a well-known, persistent primary key in the TPM's storage hierarchy.
	SRK KeyType = "srk"
)

// KeyAlgorithm represents the cryptographic algorithm used for keys
type KeyAlgorithm string

const (
	ECDSA KeyAlgorithm = "ecdsa"
	RSA   KeyAlgorithm = "rsa"
)

// Storage handles pure disk persistence of TPM data on disk
type Storage interface {
	// GetKey retrieves stored key data for the specified key type
	// Returns nil values if key doesn't exist
	GetKey(keyType KeyType) (*tpm2.TPM2BPublic, *tpm2.TPM2BPrivate, error)
	// StoreKey stores key data for the specified key type
	StoreKey(keyType KeyType, public tpm2.TPM2BPublic, private tpm2.TPM2BPrivate) error
	// GetPassword retrieves the stored storage hierarchy password
	GetPassword() ([]byte, error)
	// StorePassword stores the storage hierarchy password
	StorePassword(password []byte) error
	// ClearPassword removes the stored password
	ClearPassword() error
	// Close closes the storage and releases any resources
	Close() error
}

// Session manages active TPM state and operations
type Session interface {
	// GetHandle returns the active handle for a key type
	GetHandle(keyType KeyType) (*tpm2.NamedHandle, error)
	// CreateKey creates a new key of the specified type
	CreateKey(keyType KeyType) (*tpm2.CreateResponse, error)
	// LoadKey loads a key into the TPM and returns its handle
	LoadKey(keyType KeyType) (*tpm2.NamedHandle, error)
	// CertifyKey certifies a key with the LAK
	CertifyKey(keyType KeyType, qualifyingData []byte) (certifyInfo, signature []byte, err error)
	// Sign signs data with the specified key
	Sign(keyType KeyType, digest []byte) ([]byte, error)
	// GetPublicKey gets the public key for a key type
	GetPublicKey(keyType KeyType) (*tpm2.TPM2BPublic, error)
	// GetEndorsementKeyCert returns the endorsement key certificate
	GetEndorsementKeyCert() ([]byte, error)
	// GetEndorsementKeyPublic returns the endorsement key public data
	GetEndorsementKeyPublic() ([]byte, error)
	// FlushAllTransientHandles aggressively flushes all transient handles
	FlushAllTransientHandles() error
	// Clear performs a best-effort clear of the TPM, resetting keys and auth
	Clear() error
	// Close closes the session and flushes handles
	Close() error
}

// tpmIndexRegex matches explicitly tpm (not tpmrm!) and captures the tpm's index
var tpmIndexRegex = regexp.MustCompile(`^tpm(\d+)$`)

func createPCRSelection(selection [3]byte) *tpm2.TPMLPCRSelection {
	return &tpm2.TPMLPCRSelection{
		PCRSelections: []tpm2.TPMSPCRSelection{
			{
				Hash:      tpm2.TPMAlgSHA256,
				PCRSelect: selection[:],
			},
		},
	}
}

// createFullPCRSelection creates a PCR selection that includes all PCRs (0-23)
func createFullPCRSelection() *tpm2.TPMLPCRSelection {
	// PCRs 0-7 (all bits set)
	// PCRs 8-15 (all bits set)
	// PCRs 16-23 (all bits set)
	return createPCRSelection([3]byte{0xFF, 0xFF, 0xFF})
}

// convertTPMLPCRSelectionToPCRSelection converts tpm2.TPMLPCRSelection to tpm2.PCRSelection
// format expected by client.ReadPCRs function.
func convertTPMLPCRSelectionToPCRSelection(tpmlSelection *tpm2.TPMLPCRSelection) legacy.PCRSelection {
	if tpmlSelection == nil || len(tpmlSelection.PCRSelections) == 0 {
		return legacy.PCRSelection{}
	}

	// Use the first PCR selection (most common case)
	sel := tpmlSelection.PCRSelections[0]

	// Convert bitmask to slice of PCR indices
	var pcrs []int
	for byteIdx, b := range sel.PCRSelect {
		for bitIdx := 0; bitIdx < 8; bitIdx++ {
			if b&(1<<bitIdx) != 0 {
				pcrIndex := byteIdx*8 + bitIdx
				pcrs = append(pcrs, pcrIndex)
			}
		}
	}

	// Convert hash algorithm from tpm2.TPMAlgID to legacy.Algorithm
	var hash legacy.Algorithm
	switch sel.Hash {
	case tpm2.TPMAlgSHA1:
		hash = legacy.AlgSHA1
	case tpm2.TPMAlgSHA256:
		hash = legacy.AlgSHA256
	case tpm2.TPMAlgSHA384:
		hash = legacy.AlgSHA384
	case tpm2.TPMAlgSHA512:
		hash = legacy.AlgSHA512
	default:
		// Default to SHA256 if unknown algorithm
		hash = legacy.AlgSHA256
	}

	return legacy.PCRSelection{
		Hash: hash,
		PCRs: pcrs,
	}
}
