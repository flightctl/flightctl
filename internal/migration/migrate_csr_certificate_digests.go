package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const csrCertificateDigestBatchSize = 500

type csrCertificateDigestRow struct {
	OrgID       uuid.UUID
	Name        string
	Certificate []byte
}

type csrCertificateDigestCursor struct {
	OrgID uuid.UUID
	Name  string
}

func migrateCSRCertificateDigests(ctx context.Context, db *gorm.DB, enrollmentSignerName string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.SchemaMigration{
			Key: "csr_certificate_digests_v1", AppliedAt: time.Now(),
		})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		if err := tx.Exec("LOCK TABLE certificate_signing_requests IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
			return err
		}
		var last *csrCertificateDigestCursor
		for {
			var rows []csrCertificateDigestRow
			query := tx.Table("certificate_signing_requests").
				Select("org_id, name, status -> 'certificate' AS certificate").
				Where("certificate_digest IS NULL AND spec->>'signerName' = ? AND status ? 'certificate' AND status->>'certificate' IS NOT NULL AND status->>'certificate' <> ''", enrollmentSignerName).
				Order("org_id, name").
				Limit(csrCertificateDigestBatchSize)
			if last != nil {
				query = query.Where("(org_id, name) > (?, ?)", last.OrgID, last.Name)
			}
			if err := query.Find(&rows).Error; err != nil {
				return fmt.Errorf("read retained certificates for digest migration: %w", err)
			}
			if len(rows) == 0 {
				return nil
			}
			if err := updateCSRCertificateDigestBatch(tx, rows, enrollmentSignerName); err != nil {
				return err
			}
			last = &csrCertificateDigestCursor{
				OrgID: rows[len(rows)-1].OrgID,
				Name:  rows[len(rows)-1].Name,
			}
		}
	})
}

func updateCSRCertificateDigestBatch(tx *gorm.DB, rows []csrCertificateDigestRow, enrollmentSignerName string) error {
	values := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*4)
	for _, row := range rows {
		var certificate []byte
		if err := json.Unmarshal(row.Certificate, &certificate); err != nil {
			return fmt.Errorf("invalid retained certificate encoding for CSR %q (org %s)", row.Name, row.OrgID)
		}
		values = append(values, "(?::uuid, ?::text, ?::jsonb, ?::text)")
		args = append(args,
			row.OrgID,
			row.Name,
			string(row.Certificate),
			model.CertificateSigningRequestCertificateDigest(&certificate),
		)
	}
	if len(values) == 0 {
		return nil
	}
	args = append(args, enrollmentSignerName)
	result := tx.Exec(fmt.Sprintf(`
		UPDATE certificate_signing_requests AS csr
		SET certificate_digest = batch.digest
		FROM (VALUES %s) AS batch(org_id, name, certificate, digest)
		WHERE csr.org_id = batch.org_id
			AND csr.name = batch.name
			AND csr.spec->>'signerName' = ?
			AND csr.status -> 'certificate' = batch.certificate`,
		strings.Join(values, ", ")), args...)
	if result.Error != nil {
		return fmt.Errorf("store retained certificate digests: %w", result.Error)
	}
	if result.RowsAffected != int64(len(values)) {
		return fmt.Errorf("stored %d of %d retained certificate digests", result.RowsAffected, len(values))
	}
	return nil
}
