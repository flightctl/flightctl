package enrollmentrequest

import (
	"context"
	"crypto/x509"
	"encoding/asn1"
	"errors"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/org"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/google/uuid"
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
	csr, err := csrservice.CertificateForLeaf(ctx, h.csrService, orgID, leaf)
	if errors.Is(err, csrservice.ErrAmbiguousCertificate) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if csr == nil {
		return nil, nil
	}
	return csr.Metadata.Name, nil
}
