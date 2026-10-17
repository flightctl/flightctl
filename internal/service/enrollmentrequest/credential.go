package enrollmentrequest

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/asn1"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
)

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
	retained, err := h.csrStore.List(ctx, orgID, store.ListParams{})
	if err != nil {
		return nil, err
	}
	var resolved *string
	for _, request := range retained.Items {
		if request.Spec.SignerName != h.ca.Cfg.DeviceEnrollmentSignerName || request.Status == nil || request.Status.Certificate == nil {
			continue
		}
		issued, err := fcrypto.ParseCertificatePEM(*request.Status.Certificate)
		if err != nil || !bytes.Equal(issued.Raw, leaf.Raw) {
			continue
		}
		if resolved != nil {
			return nil, nil
		}
		resolved = request.Metadata.Name
	}
	return resolved, nil
}
