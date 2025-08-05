package tpm

import (
	"fmt"

	"github.com/google/go-tpm/tpm2"
)

// LDevIDTemplate generates a Local Device Identity key template based on the specified algorithm.
// This key template uses the Storage Root Key as the parent key.
// Key attributes are aligned with definitions from https://trustedcomputinggroup.org/wp-content/uploads/TCG_TPM-2p0-DevID_v1p00_r10_12july2021.pdf.
// Specifically, for key attribute and parameter recommendations, see Sections 7.3.4.1 and 7.3.4.3.
func LDevIDTemplate(keyAlgo KeyAlgorithm) (tpm2.TPMTPublic, error) {
	baseAttributes := tpm2.TPMAObject{
		FixedTPM:             true,  // true = must stay in TPM
		STClear:              false, // true = cannot be loaded after tpm2_clear
		FixedParent:          true,  // true = can't be re-parented
		SensitiveDataOrigin:  true,  // true = TPM generates all sensitive data during creation
		UserWithAuth:         true,  // true = pw or hmac can be used in addition to authpolicy
		AdminWithPolicy:      false, // true = authValue cannot be used for auth (temporarily disabled for testing)
		NoDA:                 false, // true = there are dictionary attack protections
		EncryptedDuplication: false, // true = there are more robust protections for duplication
		Restricted:           false, // true = cannot be used to sign data from outside tpm
		Decrypt:              false, // true = can be used to decrypt
		SignEncrypt:          true,  // true = for asymm, may be used to sign
	}

	switch keyAlgo {
	case ECDSA:
		return tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgECC,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: baseAttributes,
			Parameters: tpm2.NewTPMUPublicParms(
				tpm2.TPMAlgECC,
				&tpm2.TPMSECCParms{
					Scheme: tpm2.TPMTECCScheme{
						Scheme: tpm2.TPMAlgECDSA,
						Details: tpm2.NewTPMUAsymScheme(
							tpm2.TPMAlgECDSA,
							&tpm2.TPMSSigSchemeECDSA{
								HashAlg: tpm2.TPMAlgSHA256,
							},
						),
					},
					CurveID: tpm2.TPMECCNistP256,
				},
			),
			Unique: tpm2.NewTPMUPublicID(
				tpm2.TPMAlgECC,
				&tpm2.TPMSECCPoint{
					X: tpm2.TPM2BECCParameter{Buffer: make([]byte, 32)},
					Y: tpm2.TPM2BECCParameter{Buffer: make([]byte, 32)},
				},
			),
		}, nil

	case RSA:
		return tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgRSA,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: baseAttributes,
			Parameters: tpm2.NewTPMUPublicParms(
				tpm2.TPMAlgRSA,
				&tpm2.TPMSRSAParms{
					Scheme: tpm2.TPMTRSAScheme{
						Scheme: tpm2.TPMAlgRSASSA,
						Details: tpm2.NewTPMUAsymScheme(
							tpm2.TPMAlgRSASSA,
							&tpm2.TPMSSigSchemeRSASSA{
								HashAlg: tpm2.TPMAlgSHA256,
							},
						),
					},
					KeyBits: 2048, // 2048-bit RSA key
				},
			),
			Unique: tpm2.NewTPMUPublicID(
				tpm2.TPMAlgRSA,
				&tpm2.TPM2BPublicKeyRSA{
					Buffer: make([]byte, 256), // 2048 bits = 256 bytes
				},
			),
		}, nil

	default:
		return tpm2.TPMTPublic{}, fmt.Errorf("unsupported key algorithm: %s", keyAlgo)
	}
}

// AttestationKeyTemplate generates a Local Attestation Key template based on the specified algorithm.
// Based on go-tpm-tools AKTemplateECC/AKTemplateRSA templates.
func AttestationKeyTemplate(keyAlgo KeyAlgorithm) (tpm2.TPMTPublic, error) {
	baseAttributes := tpm2.TPMAObject{
		SignEncrypt:         true, // true = can sign data
		Restricted:          true, // true = restricted signing key for attestation
		FixedTPM:            true, // true = must stay in TPM
		FixedParent:         true, // true = can't be re-parented
		SensitiveDataOrigin: true, // true = TPM generates sensitive data
		UserWithAuth:        true, // true = password/HMAC auth supported
	}

	switch keyAlgo {
	case ECDSA:
		return tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgECC,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: baseAttributes,
			Parameters: tpm2.NewTPMUPublicParms(
				tpm2.TPMAlgECC,
				&tpm2.TPMSECCParms{
					Scheme: tpm2.TPMTECCScheme{
						Scheme: tpm2.TPMAlgECDSA,
						Details: tpm2.NewTPMUAsymScheme(
							tpm2.TPMAlgECDSA,
							&tpm2.TPMSSigSchemeECDSA{
								HashAlg: tpm2.TPMAlgSHA256,
							},
						),
					},
					CurveID: tpm2.TPMECCNistP256,
				},
			),
			Unique: tpm2.NewTPMUPublicID(
				tpm2.TPMAlgECC,
				&tpm2.TPMSECCPoint{
					X: tpm2.TPM2BECCParameter{Buffer: make([]byte, 32)},
					Y: tpm2.TPM2BECCParameter{Buffer: make([]byte, 32)},
				},
			),
		}, nil

	case RSA:
		return tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgRSA,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: baseAttributes,
			Parameters: tpm2.NewTPMUPublicParms(
				tpm2.TPMAlgRSA,
				&tpm2.TPMSRSAParms{
					Scheme: tpm2.TPMTRSAScheme{
						Scheme: tpm2.TPMAlgRSASSA,
						Details: tpm2.NewTPMUAsymScheme(
							tpm2.TPMAlgRSASSA,
							&tpm2.TPMSSigSchemeRSASSA{
								HashAlg: tpm2.TPMAlgSHA256,
							},
						),
					},
					KeyBits: 2048, // 2048-bit RSA key
				},
			),
			Unique: tpm2.NewTPMUPublicID(
				tpm2.TPMAlgRSA,
				&tpm2.TPM2BPublicKeyRSA{
					Buffer: make([]byte, 256), // 2048 bits = 256 bytes
				},
			),
		}, nil

	default:
		return tpm2.TPMTPublic{}, fmt.Errorf("unsupported key algorithm: %s", keyAlgo)
	}
}

// EndorsementKeyTemplate generates an Endorsement Key template based on the specified algorithm.
// Endorsement keys are used for device identity and attestation operations.
func EndorsementKeyTemplate(keyAlgo KeyAlgorithm) (tpm2.TPMTPublic, error) {
	baseAttributes := tpm2.TPMAObject{
		FixedTPM:             true,  // true = must stay in TPM
		STClear:              false, // true = cannot be loaded after tpm2_clear
		FixedParent:          true,  // true = can't be re-parented
		SensitiveDataOrigin:  true,  // true = TPM generates all sensitive data during creation
		UserWithAuth:         false, // true = pw or hmac can be used in addition to authpolicy
		AdminWithPolicy:      false, // true = authValue cannot be used for auth (temporarily disabled for testing)
		NoDA:                 true,  // true = there are dictionary attack protections
		EncryptedDuplication: false, // true = there are more robust protections for duplication
		Restricted:           true,  // true = restricted key for attestation/encryption
		Decrypt:              true,  // true = can be used to decrypt
		SignEncrypt:          false, // false = endorsement keys are not signing keys
	}

	switch keyAlgo {
	case ECDSA:
		return tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgECC,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: baseAttributes,
			Parameters: tpm2.NewTPMUPublicParms(
				tpm2.TPMAlgECC,
				&tpm2.TPMSECCParms{
					Scheme: tpm2.TPMTECCScheme{
						Scheme: tpm2.TPMAlgECDH,
						Details: tpm2.NewTPMUAsymScheme(
							tpm2.TPMAlgECDH,
							&tpm2.TPMSKeySchemeECDH{HashAlg: tpm2.TPMAlgSHA256},
						),
					},
					CurveID: tpm2.TPMECCNistP256,
				},
			),
			Unique: tpm2.NewTPMUPublicID(
				tpm2.TPMAlgECC,
				&tpm2.TPMSECCPoint{
					X: tpm2.TPM2BECCParameter{Buffer: make([]byte, 32)},
					Y: tpm2.TPM2BECCParameter{Buffer: make([]byte, 32)},
				},
			),
		}, nil

	case RSA:
		return tpm2.TPMTPublic{
			Type:             tpm2.TPMAlgRSA,
			NameAlg:          tpm2.TPMAlgSHA256,
			ObjectAttributes: baseAttributes,
			Parameters: tpm2.NewTPMUPublicParms(
				tpm2.TPMAlgRSA,
				&tpm2.TPMSRSAParms{
					Scheme: tpm2.TPMTRSAScheme{
						Scheme: tpm2.TPMAlgOAEP,
						Details: tpm2.NewTPMUAsymScheme(
							tpm2.TPMAlgOAEP,
							&tpm2.TPMSEncSchemeOAEP{HashAlg: tpm2.TPMAlgSHA256},
						),
					},
					KeyBits: 2048, // 2048-bit RSA key
				},
			),
			Unique: tpm2.NewTPMUPublicID(
				tpm2.TPMAlgRSA,
				&tpm2.TPM2BPublicKeyRSA{
					Buffer: make([]byte, 256), // 2048 bits = 256 bytes
				},
			),
		}, nil

	default:
		return tpm2.TPMTPublic{}, fmt.Errorf("unsupported key algorithm: %s", keyAlgo)
	}
}
