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

type certificateExtensionValue struct {
	Value   string
	Present bool
	Valid   bool
}

type certificateIdentity struct {
	Name              *string
	NeedsLegacyLookup bool
}

// certificateIdentityExtension distinguishes absent identity from malformed identity,
// so malformed issuer metadata cannot trigger legacy recovery.
func certificateIdentityExtension(leaf *x509.Certificate, oid asn1.ObjectIdentifier) certificateExtensionValue {
	var value string
	present := false
	for _, extension := range leaf.Extensions {
		if !extension.Id.Equal(oid) {
			continue
		}
		if present {
			return certificateExtensionValue{Present: true}
		}
		present = true
		rest, err := asn1.Unmarshal(extension.Value, &value)
		if err != nil || len(rest) != 0 || value == "" || extension.Critical {
			return certificateExtensionValue{Present: true}
		}
	}
	return certificateExtensionValue{Value: value, Present: present, Valid: true}
}

// extractCertificateName trusts only tenant-bound enrollment issuer metadata;
// recovery is allowed only for an absent name, never malformed metadata.
func extractCertificateName(leaf *x509.Certificate, orgID uuid.UUID, enrollmentSigner string) certificateIdentity {
	if leaf == nil {
		return certificateIdentity{}
	}
	signerName := certificateIdentityExtension(leaf, signer.OIDSignerName)
	if !signerName.Valid || !signerName.Present || signerName.Value != enrollmentSigner {
		return certificateIdentity{}
	}
	tenant := certificateIdentityExtension(leaf, signer.OIDOrgID)
	if !tenant.Valid {
		return certificateIdentity{}
	}
	leafOrg := org.DefaultID
	if tenant.Present {
		parsed, err := uuid.Parse(tenant.Value)
		if err != nil {
			return certificateIdentity{}
		}
		leafOrg = parsed
	}
	if leafOrg != orgID {
		return certificateIdentity{}
	}
	name := certificateIdentityExtension(leaf, signer.OIDEnrollmentCSRName)
	if !name.Valid {
		return certificateIdentity{}
	}
	if name.Present {
		return certificateIdentity{Name: &name.Value}
	}
	return certificateIdentity{NeedsLegacyLookup: true}
}

// findCertificateName recovers an old leaf's unique enrollment credential CSR within its tenant;
// ambiguity yields no reference, while lookup errors propagate to the caller.
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
