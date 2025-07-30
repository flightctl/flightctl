package tpm

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"math/big"

	"github.com/google/go-tpm-tools/client"
	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

//  +---------------------------+
//  |    TPM Manufacturer       |
//  |  (Root of Trust Anchor)   |
//  +-------------+-------------+
//                |
//                v
//         +---------------+
//         |      EK       |
//         |   (in TPM)    |
//         +-------+-------+
//                 | EK Cert (X.509)
//                 v
//  +---------------------------+
//  |    Owner / Admin Domain   |
//  +-------------+-------------+
//                |
//         +------v------+
//         |     LAK     | <-- Proof of Residency
//         +------+------+
//                | Certify(LAK) signed by EK
//                v
//         +-------------+
//         |   LDevID    | <-- Proof of Residency
//         +------+------+
//                | Certify(LDevID) signed by EK
//                v
//      +--------------------+
//      |   CSR signed by    |
//      | LDevID private key |
//      +--------------------+

// AttestationBundle contains the structured data required for TCG spec compliance
// This includes the TPM2_Certify results for both LAK and LDevID keys
type AttestationBundle struct {
	// EKCert is the Endorsement Key certificate from the TPM manufacturer
	EKCert []byte
	// LAKCertifyInfo contains the TPM2_Certify result for the LAK signed by EK
	LAKCertifyInfo []byte
	// LAKCertifySignature is the signature over LAKCertifyInfo made by the EK
	LAKCertifySignature []byte
	// LDevIDCertifyInfo contains the TPM2_Certify result for the LDevID signed by EK
	LDevIDCertifyInfo []byte
	// LDevIDCertifySignature is the signature over LDevIDCertifyInfo made by the EK
	LDevIDCertifySignature []byte
	// LAKPublicKey is the public portion of the LAK
	LAKPublicKey []byte
	// LDevIDPublicKey is the public portion of the LDevID
	LDevIDPublicKey []byte
}

// CertifyLAKWithEK uses TPM2_Certify to prove the LAK was created by this TPM
// This implements §5.6, §5.3 of the TCG spec using AK for signing
func (t *Client) CertifyLAKWithEK(qualifyingData []byte) ([]byte, []byte, error) {
	if t.lak == nil {
		return nil, nil, fmt.Errorf("LAK not initialized")
	}

	// use AK for signing instead of EK as per TCG spec
	ak, err := t.getAKForSigning()
	if err != nil {
		return nil, nil, fmt.Errorf("getting AK for signing: %w", err)
	}
	defer ak.Close()

	// AK signs attestation that LAK was created by this TPM
	certifyCmd := tpm2.Certify{
		ObjectHandle: tpm2.NamedHandle{
			Handle: tpm2.TPMHandle(t.lak.Handle().HandleValue()),
			Name:   tpm2.TPM2BName{Buffer: []byte{}}, // LAK name computed by TPM
		},
		SignHandle: tpm2.NamedHandle{
			Handle: tpm2.TPMHandle(ak.Handle().HandleValue()),
			Name:   tpm2.TPM2BName{Buffer: []byte{}}, // AK name computed by TPM
		},
		QualifyingData: tpm2.TPM2BData{Buffer: qualifyingData},
		InScheme: tpm2.TPMTSigScheme{
			Scheme: tpm2.TPMAlgRSASSA, // rsa scheme for rsa ak
			Details: tpm2.NewTPMUSigScheme(
				tpm2.TPMAlgRSASSA,
				&tpm2.TPMSSchemeHash{HashAlg: tpm2.TPMAlgSHA256},
			),
		},
	}

	response, err := certifyCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, nil, fmt.Errorf("TPM2_Certify failed for LAK: %w", err)
	}

	// extract the TPMS_ATTEST structure and signature
	certifyInfoBytes := tpm2.Marshal(response.CertifyInfo)
	signatureBytes := tpm2.Marshal(response.Signature)

	return certifyInfoBytes, signatureBytes, nil
}

// CertifyLDevIDWithEK uses TPM2_Certify to prove the LDevID was created by this TPM
// This implements §5.5, §5.2 of the TCG spec using AK for signing (correct pattern)
func (t *Client) CertifyLDevIDWithEK(qualifyingData []byte) ([]byte, []byte, error) {
	if t.ldevid == nil {
		return nil, nil, fmt.Errorf("LDevID not initialized")
	}

	ak, err := t.getAKForSigning()
	if err != nil {
		return nil, nil, fmt.Errorf("getting AK for signing: %w", err)
	}
	defer ak.Close()

	// AK signs attestation that LDevID was created by this TPM
	// LDevID now has AdminWithPolicy: false (like LAK), so use NamedHandle like LAK
	// TODO: this will change when we have password auth
	certifyCmd := tpm2.Certify{
		ObjectHandle: tpm2.NamedHandle{
			Handle: t.ldevid.Handle,
			Name:   t.ldevid.Name,
		},
		SignHandle: tpm2.NamedHandle{
			Handle: tpm2.TPMHandle(ak.Handle().HandleValue()),
			Name:   tpm2.TPM2BName{Buffer: []byte{}}, // AK name computed by TPM
		},
		QualifyingData: tpm2.TPM2BData{Buffer: qualifyingData},
		InScheme: tpm2.TPMTSigScheme{
			Scheme: tpm2.TPMAlgRSASSA, // rsa
			Details: tpm2.NewTPMUSigScheme(
				tpm2.TPMAlgRSASSA,
				&tpm2.TPMSSchemeHash{HashAlg: tpm2.TPMAlgSHA256},
			),
		},
	}

	response, err := certifyCmd.Execute(transport.FromReadWriter(t.conn))
	if err != nil {
		return nil, nil, fmt.Errorf("TPM2_Certify failed for LDevID: %w", err)
	}

	// extract the TPMS_ATTEST structure and signature
	certifyInfoBytes := tpm2.Marshal(response.CertifyInfo)
	signatureBytes := tpm2.Marshal(response.Signature)

	return certifyInfoBytes, signatureBytes, nil
}

// GetTCGCompliantAttestation creates a complete attestation bundle according to TCG spec
// This implements the structured data requirements from §5.7
func (t *Client) GetTCGCompliantAttestation(qualifyingData []byte) (*AttestationBundle, error) {
	ekCert, err := t.EndorsementKeyCert()
	if err != nil {
		return nil, fmt.Errorf("getting EK certificate: %w", err)
	}

	// certify LAK with EK
	lakCertifyInfo, lakCertifySignature, err := t.CertifyLAKWithEK(qualifyingData)
	if err != nil {
		return nil, fmt.Errorf("certifying LAK with EK: %w", err)
	}

	// certify LDevID with EK
	ldevidCertifyInfo, ldevidCertifySignature, err := t.CertifyLDevIDWithEK(qualifyingData)
	if err != nil {
		return nil, fmt.Errorf("certifying LDevID with EK: %w", err)
	}

	lakPubKey := t.lak.PublicKey()

	ldevidPubKey := t.ldevidPub

	lakPubKeyDER, err := x509.MarshalPKIXPublicKey(lakPubKey)
	if err != nil {
		return nil, fmt.Errorf("marshaling LAK public key: %w", err)
	}

	ldevidPubKeyDER, err := x509.MarshalPKIXPublicKey(ldevidPubKey)
	if err != nil {
		return nil, fmt.Errorf("marshaling LDevID public key: %w", err)
	}

	return &AttestationBundle{
		EKCert:                 ekCert,
		LAKCertifyInfo:         lakCertifyInfo,
		LAKCertifySignature:    lakCertifySignature,
		LDevIDCertifyInfo:      ldevidCertifyInfo,
		LDevIDCertifySignature: ldevidCertifySignature,
		LAKPublicKey:           lakPubKeyDER,
		LDevIDPublicKey:        ldevidPubKeyDER,
	}, nil
}

// VerifyAttestationBundle validates that an attestation bundle meets TCG spec requirements
// This implements the verification logic from §5.7 Line C
func VerifyAttestationBundle(bundle *AttestationBundle, trustedRoots *x509.CertPool) error {
	if bundle == nil {
		return fmt.Errorf("attestation bundle is nil")
	}

	ekCert, err := x509.ParseCertificate(bundle.EKCert)
	if err != nil {
		return fmt.Errorf("parsing EK certificate: %w", err)
	}

	// verify EK certificate chain against trusted roots
	opts := x509.VerifyOptions{
		Roots: trustedRoots,
	}
	_, err = ekCert.Verify(opts)
	if err != nil {
		return fmt.Errorf("verifying EK certificate chain: %w", err)
	}

	// parse LAK certify info to verify it was signed by the EK
	err = verifyTPM2CertifySignature(
		bundle.LAKCertifyInfo,
		bundle.LAKCertifySignature,
		ekCert.PublicKey,
	)
	if err != nil {
		return fmt.Errorf("verifying LAK certify signature: %w", err)
	}

	// parse LDevID certify info to verify it was signed by the EK
	err = verifyTPM2CertifySignature(
		bundle.LDevIDCertifyInfo,
		bundle.LDevIDCertifySignature,
		ekCert.PublicKey,
	)
	if err != nil {
		return fmt.Errorf("verifying LDevID certify signature: %w", err)
	}

	return nil
}

// verifyTPM2CertifySignature verifies a TPM2_Certify signature according to TCG specification
func verifyTPM2CertifySignature(certifyInfo, signature []byte, signingPublicKey crypto.PublicKey) error {
	if len(certifyInfo) == 0 {
		return fmt.Errorf("empty certify info")
	}
	if len(signature) == 0 {
		return fmt.Errorf("empty signature")
	}
	if signingPublicKey == nil {
		return fmt.Errorf("nil signing public key")
	}

	// hash the certifyInfo and verify the signature against that hash
	hash := sha256.Sum256(certifyInfo)

	// verify signature based on key type using standard library functions
	switch key := signingPublicKey.(type) {
	case *rsa.PublicKey:
		// try PKCS#1 v1.5 first (most common for TPM)
		err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature)
		if err != nil {
			// try PSS as fallback
			opts := &rsa.PSSOptions{
				SaltLength: rsa.PSSSaltLengthEqualsHash,
				Hash:       crypto.SHA256,
			}
			err = rsa.VerifyPSS(key, crypto.SHA256, hash[:], signature, opts)
			if err != nil {
				return fmt.Errorf("RSA signature verification failed: %w", err)
			}
		}
		return nil
	case *ecdsa.PublicKey:
		// for TPM2_Certify signatures, parse raw r,s values
		if len(signature)%2 != 0 {
			return fmt.Errorf("invalid ECDSA signature length: %d", len(signature))
		}
		sigLen := len(signature) / 2
		if sigLen == 0 {
			return fmt.Errorf("invalid ECDSA signature: too short")
		}
		r := new(big.Int).SetBytes(signature[:sigLen])
		s := new(big.Int).SetBytes(signature[sigLen:])

		if !ecdsa.Verify(key, hash[:], r, s) {
			return fmt.Errorf("ECDSA signature verification failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported public key type: %T", signingPublicKey)
	}
}

// getAKForSigning returns an AK suitable for signing operations
// AK is the correct key to use for TPM2_Certify operations per TCG spec
func (t *Client) getAKForSigning() (*client.Key, error) {
	// Use existing AK infrastructure from go-tpm-tools
	// This handles proper AK creation and authorization
	return client.AttestationKeyRSA(t.conn)
}
