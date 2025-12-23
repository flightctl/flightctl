package crypto

import (
	"encoding/base64"
	"fmt"
)

// EnrollmentCredential contains the generated enrollment credentials for a device image
type EnrollmentCredential struct {
	// CertificatePEM is the signed enrollment certificate in PEM format
	CertificatePEM []byte
	// PrivateKeyPEM is the private key in PEM format
	PrivateKeyPEM []byte
	// CABundlePEM is the CA bundle in PEM format
	CABundlePEM []byte
	// EnrollmentEndpoint is the enrollment service URL
	EnrollmentEndpoint string
	// EnrollmentUIEndpoint is the enrollment UI URL
	EnrollmentUIEndpoint string
	// CSRName is the name of the CertificateSigningRequest that was used to generate this credential
	// This allows traceability back to the CSR resource in the database
	CSRName string
}

// ToAgentConfig converts the enrollment credential to an agent config.yaml content
// that can be embedded in device images for early binding.
func (ec *EnrollmentCredential) ToAgentConfig() ([]byte, error) {
	// Create the config in the expected format for the agent
	// Using embedded format (data instead of file references)
	config := fmt.Sprintf(`enrollment-service:
  authentication:
    client-certificate-data: %s
    client-key-data: %s
  service:
    certificate-authority-data: %s
    server: %s
  enrollment-ui-endpoint: %s
`,
		base64.StdEncoding.EncodeToString(ec.CertificatePEM),
		base64.StdEncoding.EncodeToString(ec.PrivateKeyPEM),
		base64.StdEncoding.EncodeToString(ec.CABundlePEM),
		ec.EnrollmentEndpoint,
		ec.EnrollmentUIEndpoint,
	)

	return []byte(config), nil
}
