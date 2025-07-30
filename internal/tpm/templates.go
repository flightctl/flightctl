package tpm

import (
	"github.com/google/go-tpm/tpm2"
)

// This key template uses the Storage Root Key as the parent key.
// Other key attributes are aligned with definitions from https://trustedcomputinggroup.org/wp-content/uploads/TCG_TPM-2p0-DevID_v1p00_r10_12july2021.pdf. Specifically, for key attribute and parameter recommendations, see Sections 7.3.4.1 and 7.3.4.3.
var (
	LDevIDTemplate = tpm2.TPMTPublic{
		Type:    tpm2.TPMAlgECC,
		NameAlg: tpm2.TPMAlgSHA256,
		ObjectAttributes: tpm2.TPMAObject{
			FixedTPM:             true,  //true = must stay in TPM
			STClear:              false, //true = cannot be loaded after tpm2_clear
			FixedParent:          true,  //true = can't be re-parented
			SensitiveDataOrigin:  true,  //true = TPM generates all sensitive data during creation
			UserWithAuth:         true,  //true = pw or hmac can be used in addition to authpolicy
			AdminWithPolicy:      false, //false = authValue CAN be used for auth (like LAK)
			NoDA:                 false, //true = there are dictionary attack protections
			EncryptedDuplication: false, //true = there are more robust protections for duplication
			Restricted:           false, //true = cannot be used to sign data from outside tpm
			Decrypt:              false, //true = can be used to decrypt
			SignEncrypt:          true,  //true = for asymm, may be used to sign
		},
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
	}
)
