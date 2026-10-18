package certificatesigningrequest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
)

// FindNameByCertificate recovers a unique exact retained leaf among tenant-local digest candidates.
func (s *CertificateSigningRequestStore) FindNameByCertificate(ctx context.Context, orgId uuid.UUID, leafDER []byte) (resolved *string, err error) {
	digest := sha256.Sum256(leafDER)
	filter := selector.NewFieldSelectorFromMapOrDie(map[string]string{
		"internal.certificateDigest": hex.EncodeToString(digest[:]),
	}, selector.WithPrivateSelectors())
	candidates, err := s.List(ctx, orgId, store.ListParams{FieldSelector: filter})
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates.Items {
		if candidate.Status == nil || candidate.Status.Certificate == nil {
			continue
		}
		issued, err := fcrypto.ParseCertificatePEM(*candidate.Status.Certificate)
		if err != nil || !bytes.Equal(issued.Raw, leafDER) {
			continue
		}
		if resolved != nil {
			return nil, nil
		}
		resolved = candidate.Metadata.Name
	}
	return resolved, nil
}
