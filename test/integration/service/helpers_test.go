package service_test

import (
	"crypto"
	"encoding/base32"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1beta1"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// AnyPtr is a helper function to create *interface{} from any value, reducing verbosity
// in patch operations and other places where Go requires *interface{}
func AnyPtr(v interface{}) *interface{} {
	return &v
}

// NewLabelPatch creates a patch request to add a single label
func NewLabelPatch(key, value string) api.PatchRequest {
	return api.PatchRequest{{
		Op:    "add",
		Path:  fmt.Sprintf("/metadata/labels/%s", key),
		Value: AnyPtr(value),
	}}
}

// NewReplaceLabelPatch creates a patch request to replace a single label
func NewReplaceLabelPatch(key, value string) api.PatchRequest {
	return api.PatchRequest{{
		Op:    "replace",
		Path:  fmt.Sprintf("/metadata/labels/%s", key),
		Value: AnyPtr(value),
	}}
}

// NewMultiLabelPatch creates a patch request with multiple label operations
func NewMultiLabelPatch(addLabels map[string]string, replaceLabels map[string]string) api.PatchRequest {
	var patch api.PatchRequest

	for key, value := range addLabels {
		patch = append(patch, struct {
			Op    api.PatchRequestOp `json:"op"`
			Path  string             `json:"path"`
			Value *interface{}       `json:"value,omitempty"`
		}{
			Op:    "add",
			Path:  fmt.Sprintf("/metadata/labels/%s", key),
			Value: AnyPtr(value),
		})
	}

	for key, value := range replaceLabels {
		patch = append(patch, struct {
			Op    api.PatchRequestOp `json:"op"`
			Path  string             `json:"path"`
			Value *interface{}       `json:"value,omitempty"`
		}{
			Op:    "replace",
			Path:  fmt.Sprintf("/metadata/labels/%s", key),
			Value: AnyPtr(value),
		})
	}

	return patch
}

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

// IsStatusSuccessful returns true for HTTP-style success codes (2xx).
func IsStatusSuccessful(status *api.Status) bool {
	return status != nil && status.Code >= 200 && status.Code < 300
}

// CreateTestCSR returns a minimal, valid CertificateSigningRequest resource for use in tests.
func CreateTestCSR() api.CertificateSigningRequest {
	name, csrData := GenerateDeviceNameAndCSR()
	return api.CertificateSigningRequest{
		ApiVersion: "v1beta1",
		Kind:       "CertificateSigningRequest",
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
			Labels: &map[string]string{
				"test": "integration",
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
		ApiVersion: "v1beta1",
		Kind:       "EnrollmentRequest",
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr(name),
			Labels: &map[string]string{
				"test": "integration",
			},
		},
		Spec: api.EnrollmentRequestSpec{
			Csr: string(csrData),
		},
	}
}

// VerifyCSRSpecImmutability asserts that all spec fields of a CSR remained unchanged.
func VerifyCSRSpecImmutability(actual *api.CertificateSigningRequest, expected *api.CertificateSigningRequest) {
	Expect(actual.Spec.Request).To(Equal(expected.Spec.Request), "CSR request field should be immutable")
	Expect(actual.Spec.SignerName).To(Equal(expected.Spec.SignerName), "CSR signerName field should be immutable")
	Expect(actual.Spec.Usages).To(Equal(expected.Spec.Usages), "CSR usages field should be immutable")
	Expect(actual.Spec.ExpirationSeconds).To(Equal(expected.Spec.ExpirationSeconds), "CSR expirationSeconds field should be immutable")
	Expect(actual.Status).To(Equal(expected.Status), "CSR status should not be modified by user operations")
}

// VerifyERStatusUnchanged asserts that the status stanza of an EnrollmentRequest did not change.
func VerifyERStatusUnchanged(actual *api.EnrollmentRequest, expected *api.EnrollmentRequest) {
	Expect(actual.Status).To(Equal(expected.Status), "EnrollmentRequest status should remain immutable via PATCH or PUT")
}
