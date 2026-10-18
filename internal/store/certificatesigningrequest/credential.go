package certificatesigningrequest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
)

// FindNameByCertificate recovers a unique retained leaf without hydrating CSR
// resources. Streaming bounds memory, not the number of certificates inspected.
func (s *CertificateSigningRequestStore) FindNameByCertificate(ctx context.Context, orgId uuid.UUID, leafDER []byte) (resolved *string, err error) {
	rows, err := s.getDB(ctx).Model(&model.CertificateSigningRequest{}).
		Select("name, status -> 'certificate'").
		Where("org_id = ? AND deleted_at IS NULL AND spec IS NOT NULL AND status ->> 'certificate' <> ''", orgId).Rows()
	if err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	defer func() {
		err = errors.Join(err, rows.Close())
		if err != nil {
			resolved = nil
		}
	}()
	for rows.Next() {
		var name string
		var encoded []byte
		if err := rows.Scan(&name, &encoded); err != nil {
			return nil, err
		}
		var certificate []byte
		if err := json.Unmarshal(encoded, &certificate); err != nil {
			return nil, fmt.Errorf("decode retained certificate: %w", err)
		}
		issued, err := fcrypto.ParseCertificatePEM(certificate)
		if err != nil || !bytes.Equal(issued.Raw, leafDER) {
			continue
		}
		if resolved != nil {
			return nil, rows.Err()
		}
		resolved = &name
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return resolved, nil
}
