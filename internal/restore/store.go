package restore

import (
	"context"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	orgmodel "github.com/flightctl/flightctl/internal/org/model"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/storeutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RestoreStore provides the minimal set of database operations required
// by the post-restoration preparation logic. It wraps a *gorm.DB directly
// so the restore package does not depend on internal/store.Store.
type RestoreStore struct {
	db *gorm.DB
}

// NewRestoreStore creates a RestoreStore backed by the given gorm connection.
func NewRestoreStore(db *gorm.DB) *RestoreStore {
	return &RestoreStore{db: db}
}

func (s *RestoreStore) getDB(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx)
}

// PrepareDevicesAfterRestore persists awaiting-reconnect preparation for eligible
// devices using caller-supplied product params. Clears lastSeen timestamps.
// When ExcludedLifecycleStatuses is empty, no lifecycle NOT IN filter is applied.
func (s *RestoreStore) PrepareDevicesAfterRestore(ctx context.Context, params DeviceAwaitingReconnectPrepareParams) (int64, error) {
	sql, args := buildPrepareDevicesAfterRestoreQuery(params)
	result := s.getDB(ctx).Exec(sql, args...)
	if result.Error != nil {
		return 0, storeutil.ErrorFromGormError(result.Error)
	}

	return result.RowsAffected, nil
}

// buildPrepareDevicesAfterRestoreQuery builds the bulk prepare SQL and bind args.
// Exclusion statuses occupy $4..$3+N; UpdatedStatus is always the next placeholder.
func buildPrepareDevicesAfterRestoreQuery(params DeviceAwaitingReconnectPrepareParams) (string, []any) {
	args := []any{
		params.AnnotationKey,
		params.SummaryStatus,
		params.SummaryInfo,
	}

	lifecycleExclusionClause := ""
	if len(params.ExcludedLifecycleStatuses) > 0 {
		exclusionPlaceholders := make([]string, len(params.ExcludedLifecycleStatuses))
		for i, status := range params.ExcludedLifecycleStatuses {
			exclusionPlaceholders[i] = fmt.Sprintf("$%d", i+4)
			args = append(args, status)
		}
		lifecycleExclusionClause = fmt.Sprintf(
			"AND COALESCE(status->'lifecycle'->>'status', '') NOT IN (%s)",
			strings.Join(exclusionPlaceholders, ", "),
		)
	}

	updatedStatusPlaceholder := fmt.Sprintf("$%d", len(args)+1)
	args = append(args, params.UpdatedStatus)

	sql := fmt.Sprintf(`
        WITH updated_devices AS (
		UPDATE devices 
		SET 
			annotations = COALESCE(annotations, '{}'::jsonb) || jsonb_build_object($1::text, 'true'),
			status = CASE 
				WHEN status IS NOT NULL THEN 
					status || jsonb_build_object('summary', jsonb_build_object('status', $2::text, 'info', $3::text)) || jsonb_build_object('updated', jsonb_build_object('status', %s::text))
				ELSE 
					jsonb_build_object('summary', jsonb_build_object('status', $2::text, 'info', $3::text)) || jsonb_build_object('updated', jsonb_build_object('status', %s::text))
			END,
			resource_version = COALESCE(resource_version, 0) + 1
		WHERE deleted_at IS NULL 
			%s
			AND (annotations->>$1) IS DISTINCT FROM 'true'
        RETURNING name, org_id)
        UPDATE device_timestamps dt
        SET last_seen = NULL
		FROM updated_devices ud
		WHERE dt.org_id = ud.org_id AND dt.name = ud.name
	`, updatedStatusPlaceholder, updatedStatusPlaceholder, lifecycleExclusionClause)

	return sql, args
}

// PrepareEnrollmentRequestsAfterRestore persists the awaiting-reconnect annotation
// on non-approved enrollment requests using caller-supplied product params.
func (s *RestoreStore) PrepareEnrollmentRequestsAfterRestore(ctx context.Context, params EnrollmentAwaitingReconnectPrepareParams) (int64, error) {
	db := s.getDB(ctx)

	sql := `
		UPDATE enrollment_requests 
		SET 
			annotations = COALESCE(annotations, '{}'::jsonb) || jsonb_build_object($1::text, 'true'),
			resource_version = COALESCE(resource_version, 0) + 1
		WHERE deleted_at IS NULL 
			AND (status->'approval'->>'approved' IS NULL OR status->'approval'->>'approved' != 'true')
			AND (annotations->>$1) IS DISTINCT FROM 'true'
	`

	result := db.Exec(sql, params.AnnotationKey)

	if result.Error != nil {
		return 0, storeutil.ErrorFromGormError(result.Error)
	}

	return result.RowsAffected, nil
}

// GetAllDeviceNames returns all device names for a given organization.
func (s *RestoreStore) GetAllDeviceNames(ctx context.Context, orgId uuid.UUID) ([]string, error) {
	var deviceNames []string

	err := s.getDB(ctx).Model(&model.Device{}).
		Where("org_id = ? AND deleted_at IS NULL", orgId).
		Pluck("name", &deviceNames).Error

	if err != nil {
		return nil, storeutil.ErrorFromGormError(err)
	}

	return deviceNames, nil
}

// ListOrganizations returns all organizations (no filtering).
func (s *RestoreStore) ListOrganizations(ctx context.Context) ([]*orgmodel.Organization, error) {
	var orgs []*orgmodel.Organization
	if err := s.getDB(ctx).Find(&orgs).Error; err != nil {
		return nil, storeutil.ErrorFromGormError(err)
	}
	return orgs, nil
}

// CreateEvent persists a domain event.
func (s *RestoreStore) CreateEvent(ctx context.Context, orgId uuid.UUID, resource *domain.Event) error {
	m, _ := model.NewEventFromApiResource(resource)
	m.OrgID = orgId
	if err := s.getDB(ctx).Create(&m).Error; err != nil {
		return storeutil.ErrorFromGormError(err)
	}
	return nil
}
