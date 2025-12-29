package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/util"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// GenerateEnrollmentCredential creates a new enrollment credential for image building.
// This generates a unique key pair and uses the CSR service to get a signed certificate,
// allowing devices to enroll without pre-distributed credentials.
//
// The flow:
// 1. Generate a new ECDSA key pair
// 2. Create a CSR resource and submit it via CreateCertificateSigningRequest
// 3. The CSR service auto-approves and signs enrollment certificates
// 4. Return the certificate, private key, and CA bundle
//
// Parameters:
//   - ctx: context for the operation
//   - orgId: organization ID
//   - baseName: base name for the CSR (will be made unique with UUID suffix)
//   - ownerKind: the kind of the resource that owns this CSR (e.g., "ImageBuild")
//   - ownerName: the name of the resource that owns this CSR
func (h *ServiceHandler) GenerateEnrollmentCredential(ctx context.Context, orgId uuid.UUID, baseName string, ownerKind string, ownerName string) (*crypto.EnrollmentCredential, api.Status) {
	// Generate a new ECDSA P-256 key pair
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, api.StatusInternalServerError(fmt.Sprintf("generating private key: %v", err))
	}

	// Generate unique CSR name to avoid conflicts (same pattern as CLI)
	csrName := createUniqueCsrName(baseName)

	// Create CSR template (same structure as CLI)
	csrTemplate := &x509.CertificateRequest{
		SignatureAlgorithm: x509.ECDSAWithSHA256,
		Subject: pkix.Name{
			CommonName: csrName,
		},
	}

	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, privateKey)
	if err != nil {
		return nil, api.StatusInternalServerError(fmt.Sprintf("creating CSR: %v", err))
	}

	// Encode CSR to PEM format (same structure as CLI)
	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:    "CERTIFICATE REQUEST",
		Headers: map[string]string{},
		Bytes:   csrBytes,
	})

	// Create CSR resource using the enrollment signer
	csrResource := api.CertificateSigningRequest{
		ApiVersion: api.CertificateSigningRequestAPIVersion,
		Kind:       api.CertificateSigningRequestKind,
		Metadata: api.ObjectMeta{
			Name:  &csrName,
			Owner: util.SetResourceOwner(ownerKind, ownerName),
		},
		Spec: api.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: h.ca.Cfg.DeviceEnrollmentSignerName,
			Usages:     lo.ToPtr([]string{"clientAuth", "CA:false"}),
		},
	}

	// Submit CSR through the service with internal request context
	// This ensures the Owner field is preserved (not nil'd out)
	internalCtx := context.WithValue(ctx, consts.InternalRequestCtxKey, true)
	result, status := h.CreateCertificateSigningRequest(internalCtx, orgId, csrResource)
	if status.Code != 201 && status.Code != 200 {
		return nil, status
	}

	// Verify we got a signed certificate back
	if result.Status == nil || result.Status.Certificate == nil || len(*result.Status.Certificate) == 0 {
		return nil, api.StatusInternalServerError("CSR was not signed - no certificate returned")
	}

	// Encode the private key to PEM
	privateKeyPEM, err := fccrypto.PEMEncodeKey(privateKey)
	if err != nil {
		return nil, api.StatusInternalServerError(fmt.Sprintf("encoding private key: %v", err))
	}

	// Get the CA bundle
	caBundle, err := h.ca.GetCABundle()
	if err != nil {
		return nil, api.StatusInternalServerError(fmt.Sprintf("getting CA bundle: %v", err))
	}

	return &crypto.EnrollmentCredential{
		CertificatePEM:       *result.Status.Certificate,
		PrivateKeyPEM:        privateKeyPEM,
		CABundlePEM:          caBundle,
		EnrollmentEndpoint:   h.agentEndpoint,
		EnrollmentUIEndpoint: h.uiUrl,
		CSRName:              csrName,
	}, api.StatusOK()
}

// createUniqueCsrName generates a unique CSR name by appending a UUID suffix.
// This matches the CLI's createUniqueName function behavior.
func createUniqueCsrName(name string) string {
	u := uuid.NewString()
	return name + "-" + u[:8]
}
