package certificatesigningrequest

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
)

var (
	ErrAmbiguousCertificate  = errors.New("ambiguous enrollment credential certificate")
	ErrEnrollmentCSRNotFound = errors.New("enrollment credential certificate not found")
)

// CSRForLeaf resolves the CSR that issued the exact retained leaf.
func CSRForLeaf(ctx context.Context, service Service, orgId uuid.UUID, leaf *x509.Certificate) (*domain.CertificateSigningRequest, error) {
	if leaf == nil {
		return nil, fmt.Errorf("leaf certificate is required")
	}
	filter := "internal.certificateDigest=" + crypto.CertificateDigest(leaf.Raw)
	var resolved *domain.CertificateSigningRequest
	var continuation *string
	for {
		candidates, status := service.ListCertificateSigningRequests(
			ctx,
			orgId,
			domain.ListCertificateSigningRequestsParams{FieldSelector: &filter, Continue: continuation},
			selector.WithPrivateSelectors(),
		)
		if status.Code != http.StatusOK {
			return nil, fmt.Errorf("listing enrollment credentials: %s", status.Message)
		}
		for index := range candidates.Items {
			candidate := &candidates.Items[index]
			if candidate.Status == nil || candidate.Status.Certificate == nil {
				continue
			}
			issued, err := fcrypto.ParseCertificatePEM(*candidate.Status.Certificate)
			if err != nil || !bytes.Equal(issued.Raw, leaf.Raw) {
				continue
			}
			if resolved != nil {
				return nil, ErrAmbiguousCertificate
			}
			resolved = candidate
		}
		continuation = candidates.Metadata.Continue
		if continuation == nil || *continuation == "" {
			if resolved == nil {
				return nil, ErrEnrollmentCSRNotFound
			}
			return resolved, nil
		}
	}
}
