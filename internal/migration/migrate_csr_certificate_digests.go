package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func migrateCSRCertificateDigests(ctx context.Context, db *gorm.DB) error {
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
		var lastOrg uuid.UUID
		var lastName string
		first := true
		for {
			var rows []struct {
				OrgID       uuid.UUID
				Name        string
				Certificate []byte
			}
			query := tx.Table("certificate_signing_requests").Select("org_id, name, status -> 'certificate' AS certificate").Order("org_id, name").Limit(500)
			if !first {
				query = query.Where("(org_id, name) > (?, ?)", lastOrg, lastName)
			}
			if err := query.Find(&rows).Error; err != nil {
				return fmt.Errorf("read retained certificates for digest migration: %w", err)
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				var certificate []byte
				if len(row.Certificate) != 0 {
					if err := json.Unmarshal(row.Certificate, &certificate); err != nil {
						return fmt.Errorf("invalid retained certificate encoding for CSR %q (org %s)", row.Name, row.OrgID)
					}
				}
				if err := tx.Table("certificate_signing_requests").Where("org_id = ? AND name = ?", row.OrgID, row.Name).
					Update("certificate_digest", model.CertificateSigningRequestCertificateDigest(&certificate)).Error; err != nil {
					return fmt.Errorf("store retained certificate digest: %w", err)
				}
				lastOrg, lastName = row.OrgID, row.Name
			}
			first = false
		}
	})
}
