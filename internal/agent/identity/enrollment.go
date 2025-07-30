package identity

import (
	"encoding/base64"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

// CreateEnrollmentRequest creates an enrollment request using the identity provider
// Automatically includes TPM attestation certificates if the provider supports them
func CreateEnrollmentRequest(
	log *log.PrefixLogger,
	identityProvider Provider,
	deviceStatus *v1alpha1.DeviceStatus,
	defaultLabels map[string]string,
) (*v1alpha1.EnrollmentRequest, error) {
	deviceName, err := identityProvider.GetDeviceName()
	if err != nil {
		return nil, fmt.Errorf("failed to get device name: %w", err)
	}

	csr, err := identityProvider.GenerateCSR(deviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CSR: %w", err)
	}

	req := &v1alpha1.EnrollmentRequest{
		ApiVersion: "v1alpha1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: &deviceName,
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          string(csr),
			DeviceStatus: deviceStatus,
			Labels:       &defaultLabels,
		},
	}

	// TPM certificates are best effort for enrollment. If they are not
	// available, the device will not be able to enroll as TPM verified but this
	// will be observable by the service.
	tpmProvider, ok := identityProvider.(*tpmProvider)
	if !ok {
		log.Warnf("Identity provider does not support TPM attestation")
		return req, nil
	}

	ekCert, err := tpmProvider.GetEKCert()
	if err != nil {
		log.Warnf("Failed to get EK cert (device will enroll without TPM attestation): %v", err)
		// continue with enrollment without EK certificate
	} else if len(ekCert) > 0 {
		ekCertStr := string(ekCert)
		req.Spec.EkCertificate = &ekCertStr
		log.Debugf("Successfully included EK certificate in enrollment request")
	} else {
		log.Warnf("EK certificate is empty (device will enroll without TPM attestation)")
	}

	attestationBundle, err := tpmProvider.GetTCGAttestation()
	if err != nil {
		log.Warnf("Failed to get TCG attestation bundle (device will enroll without full TPM attestation): %v", err)
	} else if attestationBundle != nil {
		log.Debugf("Successfully obtained TCG attestation bundle")

		// populate LAK (Local Attestation Key) fields
		if len(attestationBundle.LAKCertifyInfo) > 0 {
			lakCertifyInfoStr := base64.StdEncoding.EncodeToString(attestationBundle.LAKCertifyInfo)
			req.Spec.LakCertifyInfo = &lakCertifyInfoStr
			log.Debugf("Successfully included LAK certify info in enrollment request")
		}

		if len(attestationBundle.LAKCertifySignature) > 0 {
			lakCertifySignatureStr := base64.StdEncoding.EncodeToString(attestationBundle.LAKCertifySignature)
			req.Spec.LakCertifySignature = &lakCertifySignatureStr
			log.Debugf("Successfully included LAK certify signature in enrollment request")
		}

		if len(attestationBundle.LAKPublicKey) > 0 {
			lakPublicKeyStr := base64.StdEncoding.EncodeToString(attestationBundle.LAKPublicKey)
			req.Spec.LakPublicKey = &lakPublicKeyStr
			log.Debugf("Successfully included LAK public key in enrollment request")
		}

		// populate LDevID (Local Device Identity) fields
		if len(attestationBundle.LDevIDCertifyInfo) > 0 {
			ldevidCertifyInfoStr := base64.StdEncoding.EncodeToString(attestationBundle.LDevIDCertifyInfo)
			req.Spec.LdevidCertifyInfo = &ldevidCertifyInfoStr
			log.Debugf("Successfully included LDevID certify info in enrollment request")
		}

		if len(attestationBundle.LDevIDCertifySignature) > 0 {
			ldevidCertifySignatureStr := base64.StdEncoding.EncodeToString(attestationBundle.LDevIDCertifySignature)
			req.Spec.LdevidCertifySignature = &ldevidCertifySignatureStr
			log.Debugf("Successfully included LDevID certify signature in enrollment request")
		}

		if len(attestationBundle.LDevIDPublicKey) > 0 {
			ldevidPublicKeyStr := base64.StdEncoding.EncodeToString(attestationBundle.LDevIDPublicKey)
			req.Spec.LdevidPublicKey = &ldevidPublicKeyStr
			log.Debugf("Successfully included LDevID public key in enrollment request")
		}
	}

	return req, nil
}
