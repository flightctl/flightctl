package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type repositoryEncryptionResource struct {
	db  *gorm.DB
	mgr *encryption.Manager
}

func newRepositoryEncryptionResource(db *gorm.DB, mgr *encryption.Manager) *repositoryEncryptionResource {
	return &repositoryEncryptionResource{db: db, mgr: mgr}
}

func (r *repositoryEncryptionResource) Kind() string { return domain.RepositoryKind }

func (r *repositoryEncryptionResource) EncryptPaths() [][]string {
	paths, _ := model.EncryptPathsForKind(domain.RepositoryKind)
	return paths
}

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
		out = append(out, &repositoryMigratableRow{db: r.db, mgr: r.mgr, row: &rows[i]})
	}
	return out, nil
}

type repositoryMigratableRow struct {
	db  *gorm.DB
	mgr *encryption.Manager
	row *model.Repository
}

func (r *repositoryMigratableRow) OrgID() uuid.UUID { return r.row.OrgID }
func (r *repositoryMigratableRow) Name() string     { return r.row.Name }

func (r *repositoryMigratableRow) Migrate(ctx context.Context, encrypt encryption.EncryptFunc) (bool, []string, error) {
	if r.row.Spec == nil {
		return false, nil, nil
	}
	before, err := json.Marshal(r.row.Spec.Data)
	if err != nil {
		return false, nil, fmt.Errorf("marshal repository spec before migrate: %w", err)
	}

	handler := model.EncryptionHandlers()[domain.RepositoryKind]
	if handler == nil {
		return false, nil, fmt.Errorf("missing encryption handler for %s", domain.RepositoryKind)
	}
	if err := handler(ctx, r.row, encrypt); err != nil {
		return false, nil, err
	}

	after, err := json.Marshal(r.row.Spec.Data)
	if err != nil {
		return false, nil, fmt.Errorf("marshal repository spec after migrate: %w", err)
	}
	keyIDs, err := collectKeyIDsFromSpec(r.row.Spec.Data, r.EncryptPaths(), r.mgr)
	if err != nil {
		return false, nil, err
	}
	return !bytes.Equal(before, after), keyIDs, nil
}

func (r *repositoryMigratableRow) EncryptPaths() [][]string {
	paths, _ := model.EncryptPathsForKind(domain.RepositoryKind)
	return paths
}

func (r *repositoryMigratableRow) Persist(ctx context.Context) error {
	return persistMigratedSpec(ctx, r.db, r.row, r.row.OrgID, r.row.Name, r.row.ResourceVersion, r.row.Spec)
}

type authProviderEncryptionResource struct {
	db  *gorm.DB
	mgr *encryption.Manager
}

func newAuthProviderEncryptionResource(db *gorm.DB, mgr *encryption.Manager) *authProviderEncryptionResource {
	return &authProviderEncryptionResource{db: db, mgr: mgr}
}

func (r *authProviderEncryptionResource) Kind() string { return domain.AuthProviderKind }

func (r *authProviderEncryptionResource) EncryptPaths() [][]string {
	paths, _ := model.EncryptPathsForKind(domain.AuthProviderKind)
	return paths
}

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
		out = append(out, &authProviderMigratableRow{db: r.db, mgr: r.mgr, row: &rows[i]})
	}
	return out, nil
}

type authProviderMigratableRow struct {
	db  *gorm.DB
	mgr *encryption.Manager
	row *model.AuthProvider
}

func (r *authProviderMigratableRow) OrgID() uuid.UUID { return r.row.OrgID }
func (r *authProviderMigratableRow) Name() string     { return r.row.Name }

func (r *authProviderMigratableRow) Migrate(ctx context.Context, encrypt encryption.EncryptFunc) (bool, []string, error) {
	if r.row.Spec == nil {
		return false, nil, nil
	}
	before, err := json.Marshal(r.row.Spec.Data)
	if err != nil {
		return false, nil, fmt.Errorf("marshal auth provider spec before migrate: %w", err)
	}

	handler := model.EncryptionHandlers()[domain.AuthProviderKind]
	if handler == nil {
		return false, nil, fmt.Errorf("missing encryption handler for %s", domain.AuthProviderKind)
	}
	if err := handler(ctx, r.row, encrypt); err != nil {
		return false, nil, err
	}

	after, err := json.Marshal(r.row.Spec.Data)
	if err != nil {
		return false, nil, fmt.Errorf("marshal auth provider spec after migrate: %w", err)
	}
	keyIDs, err := collectKeyIDsFromSpec(r.row.Spec.Data, r.EncryptPaths(), r.mgr)
	if err != nil {
		return false, nil, err
	}
	return !bytes.Equal(before, after), keyIDs, nil
}

func (r *authProviderMigratableRow) EncryptPaths() [][]string {
	paths, _ := model.EncryptPathsForKind(domain.AuthProviderKind)
	return paths
}

func (r *authProviderMigratableRow) Persist(ctx context.Context) error {
	return persistMigratedSpec(ctx, r.db, r.row, r.row.OrgID, r.row.Name, r.row.ResourceVersion, r.row.Spec)
}

// persistMigratedSpec writes the migrated spec with optimistic concurrency.
// UpdateColumns skips GORM hooks so the encryption plugin does not re-run.
func persistMigratedSpec(ctx context.Context, db *gorm.DB, model any, orgID uuid.UUID, name string, resourceVersion *int64, spec any) error {
	expected := int64(0)
	if resourceVersion != nil {
		expected = *resourceVersion
	}
	result := db.WithContext(ctx).Model(model).
		Where("org_id = ? AND name = ? AND (resource_version IS NULL OR resource_version = ?)", orgID, name, expected).
		UpdateColumns(map[string]any{
			"spec":             spec,
			"resource_version": gorm.Expr("COALESCE(resource_version, 0) + 1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("encryption migration: concurrent update for %s/%s", orgID, name)
	}
	return nil
}
