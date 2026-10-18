package enrollmentrequest

import (
	"context"
	"crypto/x509"
	"encoding/asn1"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/google/uuid"
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

// enrollmentCertificateName consumes an authenticated TLS peer, binds its signer
// and tenant, and reads signed origin without a lookup. Only absent origin permits
// unique exact-leaf recovery; this helper does not authenticate the TLS connection.
func (h *ServiceHandler) enrollmentCertificateName(ctx context.Context, orgID uuid.UUID) (*string, error) {
	leaf, err := signer.PeerCertificateFromCtx(ctx)
	if err != nil {
		return nil, nil
	}
	signerName, present, valid := certificateIdentityExtension(leaf, signer.OIDSignerName)
	if !valid || !present || signerName != h.ca.Cfg.DeviceEnrollmentSignerName {
		return nil, nil
	}
	tenant, present, valid := certificateIdentityExtension(leaf, signer.OIDOrgID)
	if !valid {
		return nil, nil
	}
	leafOrg := org.DefaultID
	if present {
		leafOrg, err = uuid.Parse(tenant)
		if err != nil {
			return nil, nil
		}
	}
	if leafOrg != orgID {
		return nil, nil
	}
	name, present, valid := certificateIdentityExtension(leaf, signer.OIDEnrollmentCSRName)
	if !valid {
		return nil, nil
	}
	if present {
		return &name, nil
	}
	return h.csrStore.FindNameByCertificate(ctx, orgID, leaf.Raw)
}
