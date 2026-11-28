package appinspection

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/storeutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RepositoryScope struct {
	Registry string
	Port     uint16
	Path     []string
	Exact    bool
}

func repositoryPackages(db *gorm.DB, orgID uuid.UUID, scope RepositoryScope) *gorm.DB {
	refs := db.Table("application_inspection_refs").Select("inspection_id").
		Where("org_id = ? AND registry = ? AND port = ? AND role = 'package'", orgID, scope.Registry, scope.Port)
	if scope.Exact {
		return refs.Where("path = ?", model.StringArray(scope.Path))
	}
	if len(scope.Path) > 0 {
		return refs.Where("path[1:?] = ?", len(scope.Path), model.StringArray(scope.Path))
	}
	return refs
}

// FenceRepositoryClaims lets pending acquisitions restart with repaired access.
// Claims acquired after the event already see the new Repository spec.
func (s *Store) FenceRepositoryClaims(ctx context.Context, orgID uuid.UUID, scope RepositoryScope, eventTime time.Time) error {
	db := store.DB(ctx, s.db)
	return storeutil.ErrorFromGormError(db.Model(&model.ApplicationInspection{}).
		Where("org_id = ? AND claim_token IS NOT NULL AND claimed_at <= ?", orgID, eventTime).
		Where(liveWork).Where("id IN (?)", repositoryPackages(db, orgID, scope)).
		Updates(map[string]any{"claim_token": nil, "claim_until": nil}).Error)
}

func (s *Store) ListDevicesAwaitingRepository(ctx context.Context, orgID uuid.UUID, scope RepositoryScope, after string, limit int) ([]string, error) {
	if err := validatePageSize(limit); err != nil {
		return nil, err
	}
	db := store.DB(ctx, s.db)
	var names []string
	err := db.Table("application_inspection_usages AS u").Distinct("u.device_name").
		Joins("JOIN application_inspections AS a ON a.org_id = u.org_id AND a.id = u.inspection_id").
		Joins("JOIN application_inspection_continuations AS c ON c.org_id = u.org_id AND c.id = u.continuation_id").
		Where("u.org_id = ? AND c.current AND u.device_name > ? AND (u.pending OR a.state = 'finished_incomplete')", orgID, after).
		Where("u.inspection_id IN (?)", repositoryPackages(db, orgID, scope)).Order("u.device_name").Limit(limit).Pluck("u.device_name", &names).Error
	return names, storeutil.ErrorFromGormError(err)
}
