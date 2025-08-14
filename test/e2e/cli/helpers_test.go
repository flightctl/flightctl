package cli_test

import (
	"crypto"
	"encoding/base32"
	"os"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"sigs.k8s.io/yaml"
)

// GenerateDeviceNameAndCSR generates a device name (deterministic, like the agent) together
// with a matching PEM-encoded CSR using the same keypair. This is reused by multiple tests.
func GenerateDeviceNameAndCSR() (string, []byte) {
	publicKey, privateKey, err := fcrypto.NewKeyPair()
	Expect(err).ToNot(HaveOccurred())

	publicKeyHash, err := fcrypto.HashPublicKey(publicKey)
	Expect(err).ToNot(HaveOccurred())

	deviceName := strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash))

	csrPEM, err := fcrypto.MakeCSR(privateKey.(crypto.Signer), deviceName)
	Expect(err).ToNot(HaveOccurred())

	return deviceName, csrPEM
}

// Create TestCSR and write to temp file
func CreateTestCSRAndWriteToTempFile() (string, error) {
	csr := CreateTestCSR()
	csrData, err := yaml.Marshal(&csr)
	if err != nil {
		return "", err
	}
	tempFile, err := os.CreateTemp("", "csr-*.yaml")
	if err != nil {
		return "", err
	}
	_, err = tempFile.Write(csrData)
	if err != nil {
		return "", err
	}
	tempFile.Close()
	return tempFile.Name(), nil
}

// CreateTestERAndWriteToTempFile returns a minimal, valid EnrollmentRequest resource for use in tests.
func CreateTestERAndWriteToTempFile() (string, error) {
	er := CreateTestER()
	erData, err := yaml.Marshal(&er)
	if err != nil {
		return "", err
	}
	tempFile, err := os.CreateTemp("", "er-*.yaml")
	if err != nil {
		return "", err
	}
	_, err = tempFile.Write(erData)
	if err != nil {
		return "", err
	}
	tempFile.Close()
	return tempFile.Name(), nil
}

// CreateTestCSR returns a minimal, valid CertificateSigningRequest resource for use in tests.
func CreateTestCSR() api.CertificateSigningRequest {
	name, csrData := GenerateDeviceNameAndCSR()
	return api.CertificateSigningRequest{
		ApiVersion: "v1alpha1",
		Kind:       "CertificateSigningRequest",
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
			Labels: &map[string]string{
				"test": "e2e",
			},
		},
		Spec: api.CertificateSigningRequestSpec{
			Request:           csrData,
			SignerName:        "flightctl.io/enrollment",
			Usages:            &[]string{"clientAuth", "CA:false"},
			ExpirationSeconds: lo.ToPtr(int32(604800)), // 7 days
		},
	}
}

// CreateTestER returns a minimal, valid EnrollmentRequest resource for use in tests.
func CreateTestER() api.EnrollmentRequest {
	name, csrData := GenerateDeviceNameAndCSR()
	return api.EnrollmentRequest{
		ApiVersion: "v1alpha1",
		Kind:       "EnrollmentRequest",
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
			Labels: &map[string]string{
				"test": "e2e",
			},
		},
		Spec: api.EnrollmentRequestSpec{
			Csr: string(csrData),
		},
	}
}
