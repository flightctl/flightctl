package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type repositoryEncryptionResource struct {
	db      *gorm.DB
	mgr     *encryption.Manager
	handler encryption.ModelEncryptHandler
}

func newRepositoryEncryptionResource(db *gorm.DB, mgr *encryption.Manager) *repositoryEncryptionResource {
	return &repositoryEncryptionResource{
		db:      db,
		mgr:     mgr,
		handler: model.EncryptionHandlers()[domain.RepositoryKind],
	}
}

func (r *repositoryEncryptionResource) Kind() string { return domain.RepositoryKind }

func (r *repositoryEncryptionResource) NextPage(ctx context.Context, orgID uuid.UUID, afterName string, limit int) ([]EncryptionMigratableRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []model.Repository
	q := r.db.WithContext(ctx).Model(&model.Repository{}).
		Where("org_id = ? AND spec IS NOT NULL", orgID).
		Order("name ASC").
		Limit(limit)
	if afterName != "" {
		q = q.Where("name > ?", afterName)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]EncryptionMigratableRow, 0, len(rows))
	for i := range rows {
		out = append(out, &repositoryMigratableRow{db: r.db, mgr: r.mgr, handler: r.handler, row: &rows[i]})
	}
	return out, nil
}

type repositoryMigratableRow struct {
	db      *gorm.DB
	mgr     *encryption.Manager
	handler encryption.ModelEncryptHandler
	row     *model.Repository
}

func (r *repositoryMigratableRow) OrgID() uuid.UUID { return r.row.OrgID }
func (r *repositoryMigratableRow) Name() string     { return r.row.Name }

func (r *repositoryMigratableRow) Migrate(ctx context.Context, encrypt encryption.EncryptFunc) (bool, []string, error) {
	return migrateModelRow(ctx, r.row, domain.RepositoryKind, encrypt, r.mgr, r.handler)
}

func (r *repositoryMigratableRow) Persist(ctx context.Context) error {
	expected := resourceVersionValue(r.row.ResourceVersion)
	newRV := expected + 1
	r.row.ResourceVersion = &newRV
	return persistMigratedModel(ctx, r.db, r.row, r.row.OrgID, r.row.Name, expected, []string{"Spec", "ResourceVersion", "UpdatedAt"})
}

type authProviderEncryptionResource struct {
	db      *gorm.DB
	mgr     *encryption.Manager
	handler encryption.ModelEncryptHandler
}

func newAuthProviderEncryptionResource(db *gorm.DB, mgr *encryption.Manager) *authProviderEncryptionResource {
	return &authProviderEncryptionResource{
		db:      db,
		mgr:     mgr,
		handler: model.EncryptionHandlers()[domain.AuthProviderKind],
	}
}

func (r *authProviderEncryptionResource) Kind() string { return domain.AuthProviderKind }

func (r *authProviderEncryptionResource) NextPage(ctx context.Context, orgID uuid.UUID, afterName string, limit int) ([]EncryptionMigratableRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []model.AuthProvider
	q := r.db.WithContext(ctx).Model(&model.AuthProvider{}).
		Where("org_id = ? AND spec IS NOT NULL", orgID).
		Order("name ASC").
		Limit(limit)
	if afterName != "" {
		q = q.Where("name > ?", afterName)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]EncryptionMigratableRow, 0, len(rows))
	for i := range rows {
		out = append(out, &authProviderMigratableRow{db: r.db, mgr: r.mgr, handler: r.handler, row: &rows[i]})
	}
	return out, nil
}

type authProviderMigratableRow struct {
	db      *gorm.DB
	mgr     *encryption.Manager
	handler encryption.ModelEncryptHandler
	row     *model.AuthProvider
}

func (r *authProviderMigratableRow) OrgID() uuid.UUID { return r.row.OrgID }
func (r *authProviderMigratableRow) Name() string     { return r.row.Name }

func (r *authProviderMigratableRow) Migrate(ctx context.Context, encrypt encryption.EncryptFunc) (bool, []string, error) {
	return migrateModelRow(ctx, r.row, domain.AuthProviderKind, encrypt, r.mgr, r.handler)
}

func (r *authProviderMigratableRow) Persist(ctx context.Context) error {
	expected := resourceVersionValue(r.row.ResourceVersion)
	newRV := expected + 1
	r.row.ResourceVersion = &newRV
	return persistMigratedModel(ctx, r.db, r.row, r.row.OrgID, r.row.Name, expected, []string{"Spec", "ResourceVersion", "UpdatedAt"})
}

type deviceEncryptionResource struct {
	db      *gorm.DB
	mgr     *encryption.Manager
	handler encryption.ModelEncryptHandler
}

func newDeviceEncryptionResource(db *gorm.DB, mgr *encryption.Manager) *deviceEncryptionResource {
	return &deviceEncryptionResource{
		db:      db,
		mgr:     mgr,
		handler: model.EncryptionHandlers()[domain.DeviceKind],
	}
}

func (r *deviceEncryptionResource) Kind() string { return domain.DeviceKind }

func (r *deviceEncryptionResource) NextPage(ctx context.Context, orgID uuid.UUID, afterName string, limit int) ([]EncryptionMigratableRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []model.Device
	q := r.db.WithContext(ctx).Model(&model.Device{}).
		Where("org_id = ? AND (rendered_config IS NOT NULL OR rendered_applications IS NOT NULL)", orgID).
		Order("name ASC").
		Limit(limit)
	if afterName != "" {
		q = q.Where("name > ?", afterName)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]EncryptionMigratableRow, 0, len(rows))
	for i := range rows {
		out = append(out, &deviceMigratableRow{db: r.db, mgr: r.mgr, handler: r.handler, row: &rows[i]})
	}
	return out, nil
}

type deviceMigratableRow struct {
	db      *gorm.DB
	mgr     *encryption.Manager
	handler encryption.ModelEncryptHandler
	row     *model.Device
}

func (r *deviceMigratableRow) OrgID() uuid.UUID { return r.row.OrgID }
func (r *deviceMigratableRow) Name() string     { return r.row.Name }

func (r *deviceMigratableRow) Migrate(ctx context.Context, encrypt encryption.EncryptFunc) (bool, []string, error) {
	return migrateModelRow(ctx, r.row, domain.DeviceKind, encrypt, r.mgr, r.handler)
}

func (r *deviceMigratableRow) Persist(ctx context.Context) error {
	expected := resourceVersionValue(r.row.ResourceVersion)
	newRV := expected + 1
	r.row.ResourceVersion = &newRV
	return persistMigratedModel(ctx, r.db, r.row, r.row.OrgID, r.row.Name, expected, []string{"RenderedConfig", "RenderedApplications", "ResourceVersion", "UpdatedAt"})
}

func migrateModelRow(ctx context.Context, row any, kind string, encrypt encryption.EncryptFunc, mgr *encryption.Manager, handler encryption.ModelEncryptHandler) (bool, []string, error) {
	if handler == nil {
		return false, nil, fmt.Errorf("missing encryption handler for %s", kind)
	}

	before, err := json.Marshal(row)
	if err != nil {
		return false, nil, fmt.Errorf("marshal %s before migrate: %w", kind, err)
	}

	seen := map[string]struct{}{}
	wrappedEncrypt := func(ctx context.Context, data []byte) ([]byte, error) {
		_, keyID, encrypted, inspectErr := mgr.InspectEncrypted(data)
		if inspectErr != nil {
			return nil, fmt.Errorf("inspect %s value before migrate: %w", kind, inspectErr)
		}
		if encrypted && keyID != "" {
			seen[keyID] = struct{}{}
		}
		out, err := encrypt(ctx, data)
		if err != nil {
			return nil, err
		}
		_, keyID, encrypted, inspectErr = mgr.InspectEncrypted(out)
		if inspectErr != nil {
			return nil, fmt.Errorf("inspect %s value after migrate: %w", kind, inspectErr)
		}
		if encrypted && keyID != "" {
			seen[keyID] = struct{}{}
		}
		return out, nil
	}

	if err := handler(ctx, row, wrappedEncrypt); err != nil {
		return false, nil, err
	}

	after, err := json.Marshal(row)
	if err != nil {
		return false, nil, fmt.Errorf("marshal %s after migrate: %w", kind, err)
	}
	return !bytes.Equal(before, after), sortedKeys(seen), nil
}

func resourceVersionValue(rv *int64) int64 {
	if rv == nil {
		return 0
	}
	return *rv
}

// persistMigratedModel writes migrated fields with optimistic concurrency.
// Uses Updates(struct) so the GORM encryption plugin runs the normal production
// path; ProcessEncryption is idempotent for already-encrypted values.
func persistMigratedModel(ctx context.Context, db *gorm.DB, row any, orgID uuid.UUID, name string, expectedRV int64, columns []string) error {
	result := db.WithContext(ctx).Model(row).
		Where("org_id = ? AND name = ? AND (resource_version IS NULL OR resource_version = ?)", orgID, name, expectedRV).
		Select(columns).
		Updates(row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: encryption migration concurrent update for %s/%s", flterrors.ErrResourceVersionConflict, orgID, name)
	}
	return nil
}
