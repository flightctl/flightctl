package store

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type DependencyRef interface {
	InitialMigration(ctx context.Context) error
	Upsert(ctx context.Context, orgID uuid.UUID, ref *model.DependencyRef) error
	ListByRefType(ctx context.Context, orgID uuid.UUID, refType string) ([]model.DependencyRef, error)
	DeleteByFleet(ctx context.Context, orgID uuid.UUID, fleetName string) error
	DeleteByDevice(ctx context.Context, orgID uuid.UUID, deviceName string) error
	ReplaceByFleet(ctx context.Context, orgID uuid.UUID, fleetName string, refs []model.DependencyRef) error
	ReplaceDeviceRefsByFleet(ctx context.Context, orgID uuid.UUID, fleetName string, refs []model.DependencyRef) error
	ReplaceByFleetDevice(ctx context.Context, orgID uuid.UUID, fleetName, deviceName string, refs []model.DependencyRef) error
	ReplaceFleetScopedDeviceRefs(ctx context.Context, orgID uuid.UUID, deviceName string, refs []model.DependencyRef) error
	ReplaceByStandaloneDevice(ctx context.Context, orgID uuid.UUID, deviceName string, refs []model.DependencyRef) error
	BulkUpsertDeviceRefs(ctx context.Context, orgID uuid.UUID, refs []model.DependencyRef) error
	ListDueGitDependencies(ctx context.Context, orgID uuid.UUID, pollInterval time.Duration) ([]model.GitDependencyProbe, error)
	ListSecretDependencyTargets(ctx context.Context, secretNamespace, secretName, newFingerprint string) ([]model.SecretDependencyRef, error)
}

type DependencyRefStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

var _ DependencyRef = (*DependencyRefStore)(nil)

func NewDependencyRef(db *gorm.DB, log logrus.FieldLogger) DependencyRef {
	return &DependencyRefStore{dbHandler: db, log: log}
}

func (s *DependencyRefStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *DependencyRefStore) InitialMigration(ctx context.Context) error {
	return s.getDB(ctx).AutoMigrate(&model.DependencyRef{})
}

func (s *DependencyRefStore) Upsert(ctx context.Context, orgID uuid.UUID, ref *model.DependencyRef) error {
	if ref == nil {
		return fmt.Errorf("cannot upsert nil DependencyRef")
	}
	ref.OrgID = orgID
	result := s.getDB(ctx).Save(ref)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	return nil
}

func (s *DependencyRefStore) ListByRefType(ctx context.Context, orgID uuid.UUID, refType string) ([]model.DependencyRef, error) {
	var refs []model.DependencyRef
	result := s.getDB(ctx).Where("org_id = ? AND ref_type = ?", orgID, refType).Find(&refs)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	return refs, nil
}

func (s *DependencyRefStore) DeleteByFleet(ctx context.Context, orgID uuid.UUID, fleetName string) error {
	result := s.getDB(ctx).Where("org_id = ? AND fleet_name = ?", orgID, fleetName).Delete(&model.DependencyRef{})
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	return nil
}

// DeleteByDevice removes all dependency refs where device_name matches,
// regardless of fleet_name. This handles both standalone refs and
// fleet-rollout refs created for parameterized revisions.
func (s *DependencyRefStore) DeleteByDevice(ctx context.Context, orgID uuid.UUID, deviceName string) error {
	result := s.getDB(ctx).Where("org_id = ? AND device_name = ?", orgID, deviceName).Delete(&model.DependencyRef{})
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	return nil
}

// ReplaceByFleet atomically replaces fleet-level dependency refs (device_name = ”).
// Device-level refs (populated during fleet rollout) are left untouched.
func (s *DependencyRefStore) ReplaceByFleet(ctx context.Context, orgID uuid.UUID, fleetName string, refs []model.DependencyRef) error {
	return s.transactionalReplace(ctx, orgID, "org_id = ? AND fleet_name = ? AND device_name = ''", []interface{}{orgID, fleetName}, refs)
}

// ReplaceDeviceRefsByFleet atomically replaces all device-level dependency refs
// for a fleet. Fleet-level refs (device_name = ”) are left untouched.
func (s *DependencyRefStore) ReplaceDeviceRefsByFleet(ctx context.Context, orgID uuid.UUID, fleetName string, refs []model.DependencyRef) error {
	return s.transactionalReplace(ctx, orgID, "org_id = ? AND fleet_name = ? AND device_name <> ''", []interface{}{orgID, fleetName}, refs)
}

// ReplaceByFleetDevice atomically replaces device-level dependency refs for a
// single device within a fleet. Used by the device-event rollout path so that
// stale refs are cleaned when a device's resolved revision changes.
func (s *DependencyRefStore) ReplaceByFleetDevice(ctx context.Context, orgID uuid.UUID, fleetName, deviceName string, refs []model.DependencyRef) error {
	return s.transactionalReplace(ctx, orgID, "org_id = ? AND fleet_name = ? AND device_name = ?", []interface{}{orgID, fleetName, deviceName}, refs)
}

// ReplaceFleetScopedDeviceRefs atomically replaces all fleet-scoped refs for a
// device (any fleet). Used by RolloutDevice to handle fleet-move scenarios
// where the device's old-fleet refs must be cleaned alongside inserting new-fleet refs.
// Standalone refs (fleet_name=”) are left untouched.
func (s *DependencyRefStore) ReplaceFleetScopedDeviceRefs(ctx context.Context, orgID uuid.UUID, deviceName string, refs []model.DependencyRef) error {
	return s.transactionalReplace(ctx, orgID, "org_id = ? AND device_name = ? AND fleet_name <> ''", []interface{}{orgID, deviceName}, refs)
}

// ReplaceByStandaloneDevice atomically replaces all dependency refs for a
// standalone device (fleet_name = ”, device_name = deviceName).
func (s *DependencyRefStore) ReplaceByStandaloneDevice(ctx context.Context, orgID uuid.UUID, deviceName string, refs []model.DependencyRef) error {
	return s.transactionalReplace(ctx, orgID, "org_id = ? AND fleet_name = '' AND device_name = ?", []interface{}{orgID, deviceName}, refs)
}

// transactionalReplace atomically deletes rows matching the WHERE clause and
// inserts the replacement refs, all within a single transaction so readers
// never see a partially empty set.
func (s *DependencyRefStore) transactionalReplace(ctx context.Context, orgID uuid.UUID, where string, whereArgs []interface{}, refs []model.DependencyRef) error {
	return s.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(where, whereArgs...).Delete(&model.DependencyRef{}).Error; err != nil {
			return ErrorFromGormError(err)
		}
		if len(refs) == 0 {
			return nil
		}
		for i := range refs {
			refs[i].OrgID = orgID
		}
		if err := tx.Create(&refs).Error; err != nil {
			return ErrorFromGormError(err)
		}
		return nil
	})
}

// BulkUpsertDeviceRefs inserts device-level dependency refs, updating the
// revision and resource_key on conflict. Used by fleet rollout to populate
// refs for parameterized git revisions after template resolution.
func (s *DependencyRefStore) BulkUpsertDeviceRefs(ctx context.Context, orgID uuid.UUID, refs []model.DependencyRef) error {
	if len(refs) == 0 {
		return nil
	}
	for i := range refs {
		refs[i].OrgID = orgID
	}
	return s.getDB(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "org_id"}, {Name: "resource_key"}, {Name: "fleet_name"}, {Name: "device_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"revision", "ref_type", "repository_name"}),
	}).Create(&refs).Error
}

// ListDueGitDependencies returns git dependency probes that are due for
// polling. It LEFT JOINs dependency_refs with sync_states so that refs
// without a sync state row (never polled) are included, filters out
// parameterized revisions, applies the poll-interval time gate, and groups
// by (repository_name, revision) with array_agg for fan-out targets.
func (s *DependencyRefStore) ListDueGitDependencies(ctx context.Context, orgID uuid.UUID, pollInterval time.Duration) ([]model.GitDependencyProbe, error) {
	var probes []model.GitDependencyProbe
	err := s.getDB(ctx).
		Table("dependency_refs dr").
		Select(
			"dr.repository_name, dr.revision, ss.fingerprint, r.spec AS repo_spec, "+
				"array_agg(DISTINCT dr.fleet_name) FILTER (WHERE dr.fleet_name <> '' AND dr.device_name = '') AS fleet_names, "+
				"array_agg(DISTINCT dr.device_name) FILTER (WHERE dr.device_name <> '') AS device_names",
		).
		Joins("LEFT JOIN sync_states ss ON ss.org_id = dr.org_id AND ss.resource_key = dr.resource_key").
		Joins("LEFT JOIN repositories r ON r.org_id = dr.org_id AND r.name = dr.repository_name").
		Where("dr.org_id = ?", orgID).
		Where("dr.ref_type = 'git'").
		Where("dr.revision NOT LIKE ?", "%{{%").
		Where("ss.last_checked_at IS NULL OR ss.last_checked_at + ? * INTERVAL '1 second' < NOW()", pollInterval.Seconds()).
		Group("dr.repository_name, dr.revision, ss.fingerprint, r.spec").
		Scan(&probes).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	return probes, nil
}

// ListSecretDependencyTargets returns flat rows of (orgID, fleetName, deviceName)
// for all dependency_refs matching the given secret where the stored fingerprint
// differs from newFingerprint (or no fingerprint exists yet). The query is cross-org
// because the K8s informer has no org context — it only receives (namespace, name).
func (s *DependencyRefStore) ListSecretDependencyTargets(ctx context.Context, secretNamespace, secretName, newFingerprint string) ([]model.SecretDependencyRef, error) {
	var refs []model.SecretDependencyRef
	err := s.getDB(ctx).
		Table("dependency_refs dr").
		Select("dr.org_id, dr.fleet_name, dr.device_name, ss.fingerprint").
		Joins("LEFT JOIN sync_states ss ON ss.org_id = dr.org_id AND ss.resource_key = dr.resource_key").
		Where("dr.ref_type = 'secret'").
		Where("dr.secret_namespace = ?", secretNamespace).
		Where("dr.secret_name = ?", secretName).
		Where("ss.fingerprint IS NULL OR ss.fingerprint != ?", newFingerprint).
		Scan(&refs).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	return refs, nil
}
