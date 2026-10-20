package enrollmentrequest

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/asn1"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// certificateIdentityExtension distinguishes absent identity from malformed identity,
// so malformed issuer metadata cannot trigger legacy recovery.
func certificateIdentityExtension(leaf *x509.Certificate, oid asn1.ObjectIdentifier) (string, bool, bool) {
	var value string
	present := false
	for _, extension := range leaf.Extensions {
		if !extension.Id.Equal(oid) {
			continue
		}
		if present {
			return "", true, false
		}
		present = true
		rest, err := asn1.Unmarshal(extension.Value, &value)
		if err != nil || len(rest) != 0 || value == "" || extension.Critical {
			return "", true, false
		}
	}
	return value, present, true
}

func extractCertificateName(leaf *x509.Certificate, orgID uuid.UUID, enrollmentSigner string) (*string, bool) {
	if leaf == nil {
		return nil, false
	}
	signerName, present, valid := certificateIdentityExtension(leaf, signer.OIDSignerName)
	if !valid || !present || signerName != enrollmentSigner {
		return nil, false
	}
	tenant, present, valid := certificateIdentityExtension(leaf, signer.OIDOrgID)
	if !valid {
		return nil, false
	}
	leafOrg := org.DefaultID
	if present {
		parsed, err := uuid.Parse(tenant)
		if err != nil {
			return nil, false
		}
		leafOrg = parsed
	}
	if leafOrg != orgID {
		return nil, false
	}
	name, present, valid := certificateIdentityExtension(leaf, signer.OIDEnrollmentCSRName)
	if !valid {
		return nil, false
	}
	if present {
		return &name, false
	}
	return nil, true
}

func (h *ServiceHandler) findCertificateName(ctx context.Context, orgID uuid.UUID, leaf *x509.Certificate) (*string, error) {
	filter := "internal.certificateDigest=" + crypto.CertificateDigest(leaf.Raw)
	var resolved *string
	var continuation *string
	for {
		candidates, status := h.csrService.ListCertificateSigningRequests(ctx, orgID, domain.ListCertificateSigningRequestsParams{FieldSelector: &filter, Continue: continuation}, selector.WithPrivateSelectors())
		if status.Code != http.StatusOK {
			return nil, fmt.Errorf("listing enrollment credentials: %s", status.Message)
		}
		if len(candidates.Items) > 1 {
			h.log.Warn("Multiple CSR certificate digest candidates")
		}
		for _, candidate := range candidates.Items {
			if candidate.Status == nil || candidate.Status.Certificate == nil {
				continue
			}
			issued, err := fcrypto.ParseCertificatePEM(*candidate.Status.Certificate)
			if err != nil || !bytes.Equal(issued.Raw, leaf.Raw) {
				continue
			}
			if resolved != nil {
				return nil, nil
			}
			resolved = candidate.Metadata.Name
		}
		continuation = candidates.Metadata.Continue
		if lo.FromPtr(continuation) == "" {
			return resolved, nil
		}
	}
}
