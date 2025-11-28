package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeviceStatusType represents the type of device status to query
type DeviceStatusType string

const (
	DeviceStatusTypeSummary     DeviceStatusType = "summary"
	DeviceStatusTypeApplication DeviceStatusType = "application"
	DeviceStatusTypeUpdate      DeviceStatusType = "update"
)

// String returns the string representation of the status type
func (d DeviceStatusType) String() string {
	return string(d)
}

// Validate ensures the status type is valid
func (d DeviceStatusType) Validate() error {
	switch d {
	case DeviceStatusTypeSummary, DeviceStatusTypeApplication, DeviceStatusTypeUpdate:
		return nil
	default:
		return fmt.Errorf("invalid device status type: %s", d)
	}
}

type Device interface {
	InitialMigration(ctx context.Context) error

	// Exposed to users
	Create(ctx context.Context, orgId uuid.UUID, device *api.Device, eventCallback EventCallback) (*api.Device, error)
	Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, eventCallback EventCallback) (*api.Device, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, eventCallback EventCallback) (*api.Device, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error)
	Labels(ctx context.Context, orgId uuid.UUID, listParams ListParams) (api.LabelList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) (bool, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device, eventCallback EventCallback) (*api.Device, error)
	GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.Device, error)
	Healthcheck(ctx context.Context, orgId uuid.UUID, names []string) error
	ProcessAwaitingReconnectAnnotation(ctx context.Context, orgId uuid.UUID, deviceName string, deviceReportedVersion *string) (bool, error)
	GetLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*time.Time, error)

	// Used internally
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string) (string, error)
	SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition, callback ServiceConditionsCallback) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error)
	PrepareDevicesAfterRestore(ctx context.Context) (int64, error)
	RemoveConflictPausedAnnotation(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, []string, error)
	SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error
	ListDisconnected(ctx context.Context, orgId uuid.UUID, listParams ListParams, cutoffTime time.Time) (*api.DeviceList, error)
	GetWithoutServiceConditions(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error)

	// Used only by rollout
	Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error)
	UnmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) error
	MarkRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams ListParams, limit *int) error
	CompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, error)
	CountByLabels(ctx context.Context, orgId uuid.UUID, listParams ListParams, groupBy []string) ([]map[string]any, error)
	Summary(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DevicesSummary, error)

	// Used by fleet selector
	ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams ListParams) (*api.DeviceList, error)

	// Used by tests
	SetIntegrationTestCreateOrUpdateCallback(IntegrationTestCallback)
	CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, statusType DeviceStatusType, groupByFleet bool) ([]CountByOrgAndStatusResult, error)

	// Used for restoration
	GetAllDeviceNames(ctx context.Context, orgId uuid.UUID) ([]string, error)
}
type DeviceStore struct {
	dbHandler    *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.Device, model.Device, api.Device, api.DeviceList]
}

type DeviceStoreValidationCallback func(ctx context.Context, before *api.Device, after *api.Device) error
type ServiceConditionsCallback func(ctx context.Context, orgId uuid.UUID, device *api.Device, oldConditions, newConditions []api.Condition)

// Make sure we conform to Device interface
var _ Device = (*DeviceStore)(nil)

func NewDevice(db *gorm.DB, log logrus.FieldLogger) Device {
	genericStore := NewGenericStore[*model.Device, model.Device, api.Device, api.DeviceList](
		db,
		log,
		model.NewDeviceFromApiResource,
		(*model.Device).ToApiResource,
		model.DevicesToApiResource,
	)
	return &DeviceStore{dbHandler: db, log: log, genericStore: genericStore}
}

func (s *DeviceStore) callEventCallback(ctx context.Context, eventCallback EventCallback, orgId uuid.UUID, name string, oldDevice, newDevice *api.Device, created bool, err error) {
	if eventCallback == nil {
		return
	}

	SafeEventCallback(s.log, func() {
		eventCallback(ctx, api.DeviceKind, orgId, name, oldDevice, newDevice, created, err)
	})
}

func (s *DeviceStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *DeviceStore) SetIntegrationTestCreateOrUpdateCallback(c IntegrationTestCallback) {
	s.genericStore.IntegrationTestCreateOrUpdateCallback = c
}

func (s *DeviceStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.Device{}, &model.DeviceLabel{}, &model.DeviceTimestamp{}); err != nil {
		return err
	}

	if err := s.createDeviceNameIndex(db); err != nil {
		return err
	}

	if err := s.createDeviceAliasIndexes(db); err != nil {
		return err
	}

	if err := s.createDeviceLabelsIndex(db); err != nil {
		return err
	}

	if err := s.createDeviceAnnotationsIndex(db); err != nil {
		return err
	}

	if err := s.createDeviceStatusIndex(db); err != nil {
		return err
	}

	if err := s.createDeviceLabelsPartialIndex(db); err != nil {
		return err
	}

	if err := s.createServiceConditionsIndex(db); err != nil {
		return err
	}

	if err := s.createDeviceLabelsTrigger(db); err != nil {
		return err
	}

	if err := s.createDeviceTimestampInsertTrigger(db); err != nil {
		return err
	}

	if err := s.backfillDeviceTimestamps(db); err != nil {
		return err
	}

	if err := s.dropLastSeenColumnIfExists(db); err != nil {
		return err
	}

	return nil
}

func (s *DeviceStore) createDeviceNameIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.Device{}, "idx_device_primary_key_name") {
		if db.Dialector.Name() == "postgres" {
			return db.Exec("CREATE INDEX idx_device_primary_key_name ON devices USING BTREE (name)").Error
		} else {
			return db.Migrator().CreateIndex(&model.Device{}, "PrimaryKeyColumnName")
		}
	}
	return nil
}

func (s *DeviceStore) createDeviceAliasIndexes(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.Device{}, "device_alias") {
		if db.Dialector.Name() == "postgres" {
			// Enable pg_trgm extension if not already enabled
			if err := db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
				return err
			}
			// Create a B-Tree index for exact matches on the 'Alias' field
			if err := db.Exec("CREATE INDEX IF NOT EXISTS device_alias_btree ON devices USING BTREE (alias)").Error; err != nil {
				return err
			}
			// Create a GIN index for substring matches on the 'Alias' field
			return db.Exec("CREATE INDEX IF NOT EXISTS device_alias_gin ON devices USING GIN (alias gin_trgm_ops)").Error
		} else {
			return db.Migrator().CreateIndex(&model.Device{}, "device_alias")
		}
	}
	return nil
}

func (s *DeviceStore) createDeviceLabelsIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.Device{}, "idx_device_labels") {
		if db.Dialector.Name() == "postgres" {
			return db.Exec("CREATE INDEX idx_device_labels ON devices USING GIN (labels)").Error
		} else {
			return db.Migrator().CreateIndex(&model.Device{}, "Labels")
		}
	}
	return nil
}

func (s *DeviceStore) createDeviceAnnotationsIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.Device{}, "idx_device_annotations") {
		if db.Dialector.Name() == "postgres" {
			return db.Exec("CREATE INDEX idx_device_annotations ON devices USING GIN (annotations)").Error
		} else {
			return db.Migrator().CreateIndex(&model.Device{}, "Annotations")
		}
	}
	return nil
}

func (s *DeviceStore) createDeviceStatusIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.Device{}, "idx_device_status") {
		if db.Dialector.Name() == "postgres" {
			return db.Exec("CREATE INDEX idx_device_status ON devices USING GIN (status)").Error
		} else {
			return db.Migrator().CreateIndex(&model.Device{}, "Status")
		}
	}
	return nil
}

func (s *DeviceStore) createDeviceLabelsPartialIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.DeviceLabel{}, "idx_device_labels_partial") {
		if db.Dialector.Name() == "postgres" {
			// Enable pg_trgm extension for partial matching
			if err := db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
				return err
			}
			// Create GIN index for partial match searches
			return db.Exec("CREATE INDEX IF NOT EXISTS idx_device_labels_partial ON device_labels USING GIN (label_key gin_trgm_ops, label_value gin_trgm_ops)").Error
		}
	}
	return nil
}

func (s *DeviceStore) createServiceConditionsIndex(db *gorm.DB) error {
	if !db.Migrator().HasIndex(&model.Device{}, "idx_devices_service_conditions") {
		if db.Dialector.Name() == "postgres" {
			// Create a GIN index on the service_conditions JSONB field
			// This provides optimal performance for JSONB path operations
			return db.Exec("CREATE INDEX IF NOT EXISTS idx_devices_service_conditions ON devices USING GIN ((service_conditions->'conditions')) WHERE service_conditions IS NOT NULL").Error
		} else {
			return db.Migrator().CreateIndex(&model.Device{}, "ServiceConditions")
		}
	}
	return nil
}

func (s *DeviceStore) createDeviceLabelsTrigger(db *gorm.DB) error {
	if db.Dialector.Name() == "postgres" {
		triggerSQL := `
		DROP TRIGGER IF EXISTS device_labels_insert ON devices;
		DROP TRIGGER IF EXISTS device_labels_update ON devices;
	
		CREATE OR REPLACE FUNCTION sync_device_labels()
		RETURNS TRIGGER AS $$
		DECLARE
			label RECORD;
		BEGIN
			IF TG_OP = 'UPDATE' THEN
				DELETE FROM device_labels
				WHERE org_id = OLD.org_id AND device_name = OLD.name
				AND label_key NOT IN (SELECT jsonb_object_keys(NEW.labels));
			END IF;
	
			FOR label IN SELECT * FROM jsonb_each_text(NEW.labels)
			LOOP
				INSERT INTO device_labels (org_id, device_name, label_key, label_value)
				VALUES (NEW.org_id, NEW.name, label.key, label.value)
				ON CONFLICT (org_id, device_name, label_key) DO UPDATE
				SET label_value = EXCLUDED.label_value;
			END LOOP;
	
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
	
		CREATE TRIGGER device_labels_insert
		AFTER INSERT ON devices
		FOR EACH ROW
		EXECUTE FUNCTION sync_device_labels();
	
		CREATE TRIGGER device_labels_update
		AFTER UPDATE OF labels ON devices
		FOR EACH ROW
		WHEN (OLD.labels IS DISTINCT FROM NEW.labels)
		EXECUTE FUNCTION sync_device_labels();
		`
		return db.Exec(triggerSQL).Error
	}
	return nil
}

func (s *DeviceStore) createDeviceTimestampInsertTrigger(db *gorm.DB) error {
	if db.Dialector.Name() == "postgres" {
		triggerSQL := `
		DROP TRIGGER IF EXISTS device_after_insert ON devices;
		DROP FUNCTION IF EXISTS create_device_timestamp();
		CREATE OR REPLACE FUNCTION create_device_timestamp()
		RETURNS TRIGGER AS $$
		BEGIN
		    INSERT INTO device_timestamps (name, org_id)
		    VALUES (NEW.name, NEW.org_id);
		    RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER device_after_insert
		AFTER INSERT ON devices
		FOR EACH ROW
		EXECUTE FUNCTION create_device_timestamp();
		`
		return db.Exec(triggerSQL).Error
	}
	return nil
}

func (s *DeviceStore) backfillDeviceTimestamps(db *gorm.DB) error {
	return db.Exec(`INSERT INTO device_timestamps (org_id, name) 
		SELECT org_id, name FROM devices WHERE (org_id, name) NOT IN (SELECT org_id, name FROM device_timestamps)`).Error
}

func (s *DeviceStore) dropLastSeenColumnIfExists(db *gorm.DB) error {
	if db.Migrator().HasColumn(&model.Device{}, "last_seen") {
		return db.Migrator().DropColumn(&model.Device{}, "last_seen")
	}
	return nil
}

func (s *DeviceStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Device, eventCallback EventCallback) (*api.Device, error) {
	device, err := s.genericStore.Create(ctx, orgId, resource)
	name := lo.FromPtr(resource.Metadata.Name)
	s.callEventCallback(ctx, eventCallback, orgId, name, nil, device, true, err)
	return device, err
}

func (s *DeviceStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, eventCallback EventCallback) (*api.Device, error) {
	device, oldDevice, err := s.genericStore.Update(ctx, orgId, resource, fieldsToUnset, fromAPI, validationCallback)
	name := lo.FromPtr(resource.Metadata.Name)
	s.callEventCallback(ctx, eventCallback, orgId, name, oldDevice, device, false, err)
	return device, err
}

func (s *DeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, eventCallback EventCallback) (*api.Device, bool, error) {
	device, oldDevice, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, validationCallback)
	name := lo.FromPtr(resource.Metadata.Name)
	s.callEventCallback(ctx, eventCallback, orgId, name, oldDevice, device, created, err)
	return device, created, err
}

func (s *DeviceStore) getWithTimestamp(ctx context.Context, orgId uuid.UUID, name string, opts ...model.APIResourceOption) (*api.Device, error) {
	var deviceModel model.DeviceWithTimestamp
	device := s.getDB(ctx).Raw(`SELECT d.*, dt.last_seen
          FROM devices d, device_timestamps dt
          WHERE d.org_id = ? AND d.name = ? AND d.deleted_at is NULL AND 
          d.org_id = dt.org_id AND d.name = dt.name`, orgId, name).Scan(&deviceModel)
	if device.Error != nil {
		return nil, ErrorFromGormError(device.Error)
	}
	if device.RowsAffected == 0 {
		return nil, flterrors.ErrResourceNotFound
	}
	ret, err := deviceModel.ToApiResource(opts...)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s *DeviceStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *DeviceStore) GetWithoutServiceConditions(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	return s.getWithTimestamp(ctx, orgId, name, model.WithoutServiceConditions())
}

func (s *DeviceStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *DeviceStore) ListDisconnected(ctx context.Context, orgId uuid.UUID, listParams ListParams, cutoffTime time.Time) (*api.DeviceList, error) {
	var nextContinue *string
	var numRemaining *int64
	var devices []model.DeviceWithTimestamp
	queryStr := `SELECT d.*, dt.last_seen
          FROM devices d, device_timestamps dt
          WHERE d.org_id = ? AND d.deleted_at is NULL AND d.name = dt.name AND d.org_id = dt.org_id AND dt.last_seen < ? `

	args := []interface{}{
		orgId,
		cutoffTime,
	}

	if listParams.Continue != nil && len(listParams.Continue.Names) == 1 {
		queryStr += ` AND d.name >= ? `
		args = append(args, listParams.Continue.Names[0])
	}

	queryStr += ` order by d.name `

	if listParams.Limit > 0 {
		queryStr += ` limit ?`
		args = append(args, listParams.Limit+1)
	}

	if err := s.getDB(ctx).Raw(queryStr, args...).Scan(&devices).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(devices) > listParams.Limit {
		lastIndex := len(devices) - 1
		lastItem := devices[lastIndex]
		continueValues := []string{lastItem.Name}

		devices = devices[:lastIndex]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery, err := ListQuery(&model.Device{}).Build(ctx, s.getDB(ctx), orgId, listParams)
			if err != nil {
				return nil, err
			}
			numRemainingVal = CountRemainingItems(countQuery, continueValues, listParams)
		}

		nextContinue = BuildContinueString(continueValues, numRemainingVal)
		numRemaining = &numRemainingVal
	}

	ret, err := model.DevicesToApiResource(devices, nextContinue, numRemaining)
	if err != nil {
		return nil, err
	}

	return lo.ToPtr(ret), nil
}

func (s *DeviceStore) Labels(ctx context.Context, orgId uuid.UUID, listParams ListParams) (api.LabelList, error) {
	var labels []model.DeviceLabel

	resolver, err := selector.NewCompositeSelectorResolver(&model.DeviceLabel{}, &model.Device{})
	if err != nil {
		return nil, fmt.Errorf("failed to create selector resolver: %w", err)
	}

	query, err := ListQuery(model.Device{}, WithSelectorResolver(resolver)).BuildNoOrder(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return nil, err
	}

	query = query.Select("DISTINCT device_labels.label_key, device_labels.label_value").
		Joins("JOIN device_labels ON devices.org_id = device_labels.org_id AND devices.name = device_labels.device_name")

	if listParams.Limit > 0 {
		query = query.Limit(listParams.Limit)
	}

	if err := query.Find(&labels).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}

	labelStrings := make([]string, len(labels))
	for i, label := range labels {
		labelStrings[i] = fmt.Sprintf("%s=%s", label.LabelKey, label.LabelValue)
	}

	return labelStrings, nil
}

func (s *DeviceStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) (bool, error) {
	deleted, err := s.genericStore.Delete(
		ctx,
		model.Device{Resource: model.Resource{OrgID: orgId, Name: name}},
		Resource{Table: "enrollment_requests", OrgID: orgId.String(), Name: name})
	if deleted && eventCallback != nil {
		s.callEventCallback(ctx, eventCallback, orgId, name, nil, nil, false, err)
	}
	return deleted, err
}

func (s *DeviceStore) Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error) {
	query, err := ListQuery(&model.Device{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return 0, err
	}
	var devicesCount int64
	if err := query.Count(&devicesCount).Error; err != nil {
		return 0, ErrorFromGormError(err)
	}
	return devicesCount, nil
}

// CompletionCounts is used for finding if a rollout batch is complete or to set the success percentage of the batch.
// The result is a count of devices grouped by some fields:
// - rendered_template_version: taken from the annotation 'device-controller/renderedTemplateVersion'
// - summary_status: taken from the field 'status.summary.status'
// - updating_reason: it is the reason field from a condition having type 'Updating'
// - same_rendered_version: it is the result of comparison for equality between the annotation 'device-controller/renderedVersion' and the field 'status.config.renderedVersion'
// - update_timed_out: it is a boolean value indicating if the update of the device has been timed out
func (s *DeviceStore) CompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, error) {
	var (
		results            []api.DeviceCompletionCount
		updateTimeoutValue any
	)

	if updateTimeout != nil {
		updateTimeoutValue = gorm.Expr("render_timestamp < ?", time.Now().Add(-(*updateTimeout)))
	} else {
		updateTimeoutValue = gorm.Expr("false")
	}
	err := s.getDB(ctx).Raw(fmt.Sprintf(`select count(*) as count,
                                 status -> 'config' ->> 'renderedVersion' = annotations->>'%s' AS same_rendered_version,
                                 elem ->> 'reason' as updating_reason,
                                 annotations->>'%s' = ? as same_template_version,
								 ? as update_timed_out
                          from devices d LEFT JOIN LATERAL (
                            SELECT elem
						    FROM jsonb_array_elements(d.status->'conditions') AS elem
						    WHERE elem->>'type' = 'Updating'
						    LIMIT 1
							) subquery ON TRUE
						     where
						        org_id = ? and owner = ? and annotations ? '%s' and deleted_at is null
						        group by same_rendered_version, updating_reason, same_template_version, update_timed_out`,
		api.DeviceAnnotationRenderedVersion, api.DeviceAnnotationRenderedTemplateVersion, api.DeviceAnnotationSelectedForRollout),
		templateVersion,
		updateTimeoutValue,
		orgId,
		owner,
		gorm.Expr("?")).Scan(&results).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	return results, nil
}

func (s *DeviceStore) unmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) (bool, error) {
	err := s.getDB(ctx).Model(&model.Device{}).Where("org_id = ? and owner = ? and annotations ? ?",
		orgId, util.ResourceOwner(api.FleetKind, fleetName), gorm.Expr("?"), api.DeviceAnnotationSelectedForRollout).Updates(map[string]any{
		"annotations":      gorm.Expr("annotations - ?", api.DeviceAnnotationSelectedForRollout),
		"resource_version": gorm.Expr("resource_version + 1"),
	}).Error
	err = ErrorFromGormError(err)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	return false, nil
}

// UnmarkRolloutSelection unmarks all previously marked devices for rollout in a fleet
func (s *DeviceStore) UnmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) error {
	return retryUpdate(func() (bool, error) {
		return s.unmarkRolloutSelection(ctx, orgId, fleetName)
	})
}

func (s *DeviceStore) markRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams ListParams, limit *int) (bool, error) {
	query, err := ListQuery(&model.Device{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return false, err
	}
	if limit != nil {
		query = query.Limit(*limit)
		query = s.getDB(ctx).Model(&model.Device{}).Where("org_id = ? and name in (?)", orgId,
			query.Select("name"))
	}
	err = query.Updates(map[string]any{
		"annotations":      gorm.Expr(fmt.Sprintf(`jsonb_set(COALESCE(annotations, '{}'::jsonb), '{%s}', '""')`, api.DeviceAnnotationSelectedForRollout)),
		"resource_version": gorm.Expr("resource_version + 1")}).Error
	err = ErrorFromGormError(err)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	return false, nil
}

// MarkRolloutSelection marks all devices that can be filtered by the list params.  If limit is provided then the number of marked devices
// will not be greater than the provided limit.
func (s *DeviceStore) MarkRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams ListParams, limit *int) error {
	return retryUpdate(func() (bool, error) {
		return s.markRolloutSelection(ctx, orgId, listParams, limit)
	})
}

// Labels may contain characters that are not allowed to be part of a valid postgres field name.  This function
// transforms a label to a valid postgres symbol
func labelKeyToSymbol(labelKey string) string {
	var builder strings.Builder
	for _, c := range labelKey {
		switch c {
		case '.':
			builder.WriteString("_dot_")
		case '-':
			builder.WriteString("_dash_")
		case '/':
			builder.WriteString("_slash_")
		default:
			builder.WriteRune(c)
		}
	}
	return builder.String()
}

// CountByLabels is used for rollout policy disruption budget to provide device count values grouped by the label values.
func (s *DeviceStore) CountByLabels(ctx context.Context, orgId uuid.UUID, listParams ListParams, groupBy []string) ([]map[string]any, error) {
	query, err := ListQuery(&model.Device{}).BuildNoOrder(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return nil, err
	}

	selectList := lo.RepeatBy(len(groupBy), func(_ int) string { return "labels ->> ? as ?" })
	countByCondition := "count(case when ? then 1 end) as ?"
	selectList = append(selectList,
		"count(*) as total",
		countByCondition,
		countByCondition)

	labelSymbols := lo.Map(groupBy, func(s string, _ int) string { return labelKeyToSymbol(s) })

	args := lo.Interleave(lo.ToAnySlice(groupBy), lo.Map(labelSymbols, func(s string, _ int) any { return gorm.Expr(s) }))
	args = append(args, gorm.Expr("status -> 'summary' ->> 'status' <> 'Unknown'"), gorm.Expr("connected"))
	args = append(args, gorm.Expr("status -> 'summary' ->> 'status' <> 'Unknown' and status -> 'config' ->> 'renderedVersion' <> annotations ->> ?",
		api.DeviceAnnotationRenderedVersion), gorm.Expr("busy_connected"))

	query.Select(strings.Join(selectList, ","), args...)
	for _, g := range labelSymbols {
		query = query.Group(g)
	}
	var results []map[string]any
	err = query.Scan(&results).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	ret := lo.Map(results, func(m map[string]any, _ int) map[string]any {
		return lo.SliceToMap(append(groupBy, "total", "connected", "busy_connected"), func(s string) (string, any) {
			return s, m[labelKeyToSymbol(s)]
		})
	})
	return ret, nil
}

func (s *DeviceStore) Summary(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DevicesSummary, error) {
	query, err := ListQuery(&model.Device{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return nil, err
	}

	var devicesCount int64
	if err := query.Count(&devicesCount).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}

	statusCount, err := CountStatusList(ctx, query,
		"status.applicationsSummary.status",
		"status.summary.status",
		"status.updated.status")
	if err != nil {
		return nil, ErrorFromGormError(err)
	}

	applicationStatus := statusCount.List("status.applicationsSummary.status")
	summaryStatus := statusCount.List("status.summary.status")
	updateStatus := statusCount.List("status.updated.status")
	return &api.DevicesSummary{
		Total:             devicesCount,
		ApplicationStatus: applicationStatus,
		SummaryStatus:     summaryStatus,
		UpdateStatus:      updateStatus,
	}, nil
}

func (s *DeviceStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Device, eventCallback EventCallback) (*api.Device, error) {
	var oldDevice api.Device
	name := lo.FromPtr(resource.Metadata.Name)
	device, err := s.Get(ctx, orgId, name)
	if err != nil {
		s.log.Errorf("error fetching device %s/%s for update status event processing", orgId, name)
	} else if device != nil {
		// Capture old device with deep copy
		var devices []api.Device
		devices = append(devices, *device)
		oldDevice = devices[0]
	}

	device, err = s.genericStore.UpdateStatus(ctx, orgId, resource)
	s.callEventCallback(ctx, eventCallback, orgId, name, &oldDevice, device, false, err)
	return device, err
}

func (s *DeviceStore) updateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) (bool, error) {
	existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.getDB(ctx).Take(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	existingAnnotations := util.EnsureMap(existingRecord.Annotations)

	existingAnnotations = util.MergeLabels(existingAnnotations, annotations)

	for _, deleteKey := range deleteKeys {
		delete(existingAnnotations, deleteKey)
	}

	result = s.getDB(ctx).Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":      model.MakeJSONMap(existingAnnotations),
		"resource_version": gorm.Expr("resource_version + 1"),
	})

	err := ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *DeviceStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	return retryUpdate(func() (bool, error) {
		return s.updateAnnotations(ctx, orgId, name, annotations, deleteKeys)
	})
}

func (s *DeviceStore) healthcheck(ctx context.Context, orgId uuid.UUID, names []string) (bool, error) {
	// Handle empty device list gracefully
	if len(names) == 0 {
		return false, nil
	}

	result := s.getDB(ctx).Model(&model.DeviceTimestamp{}).Where("org_id = ? and name in (?)", orgId, names).Update(
		"last_seen", time.Now().UTC())
	err := ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}

	return false, nil
}

func (s *DeviceStore) Healthcheck(ctx context.Context, orgId uuid.UUID, names []string) error {
	return retryUpdate(func() (bool, error) {
		return s.healthcheck(ctx, orgId, names)
	})
}

// ProcessAwaitingReconnectAnnotation processes the AwaitingReconnect annotation for a specific device
// This is called from GetRenderedDevice when a device connects and has the awaiting-reconnect annotation
// Returns true if the device was moved to conflict paused state, false otherwise
func (s *DeviceStore) ProcessAwaitingReconnectAnnotation(ctx context.Context, orgId uuid.UUID, deviceName string, deviceReportedVersion *string) (bool, error) {
	var wasConflictPaused bool
	err := retryUpdate(func() (bool, error) {
		var retry bool
		var err error
		retry, wasConflictPaused, err = s.processAwaitingReconnectAnnotation(ctx, orgId, deviceName, deviceReportedVersion)
		return retry, err
	})
	return wasConflictPaused, err
}

func (s *DeviceStore) processAwaitingReconnectAnnotation(ctx context.Context, orgId uuid.UUID, deviceName string, deviceReportedVersion *string) (bool, bool, error) {
	s.log.Infof("Starting processAwaitingReconnectAnnotation for device %s (orgId: %s)", deviceName, orgId)

	// First, get the device to check its current annotations
	var device model.Device
	result := s.getDB(ctx).Where("org_id = ? and name = ?", orgId, deviceName).First(&device)
	if result.Error != nil {
		s.log.WithError(result.Error).Errorf("Failed to fetch device %s from database", deviceName)
		return strings.Contains(result.Error.Error(), "deadlock"), false, ErrorFromGormError(result.Error)
	}
	s.log.Infof("Successfully fetched device %s from database", deviceName)

	// Check if device has awaiting reconnect annotation
	annotations := util.EnsureMap(device.Annotations)
	waitingAnnotation, hasWaitingAnnotation := annotations[api.DeviceAnnotationAwaitingReconnect]
	s.log.Infof("Device %s awaiting reconnect annotation: hasAnnotation=%t, value=%s", deviceName, hasWaitingAnnotation, waitingAnnotation)

	if !hasWaitingAnnotation || waitingAnnotation != "true" {
		s.log.Infof("Device %s does not have awaiting reconnect annotation or value is not 'true', skipping processing", deviceName)
		return false, false, nil // No awaiting reconnect annotation, nothing to do
	}

	// Get device-reported version from params (what the device thinks its current version is)
	var deviceVersion int64 = 0
	deviceVersionStr := "0"
	if deviceReportedVersion != nil && *deviceReportedVersion != "" {
		var err error
		deviceVersion, err = strconv.ParseInt(*deviceReportedVersion, 10, 64)
		if err != nil {
			s.log.Warnf("Failed to parse device reported version '%s' for device %s: %v", *deviceReportedVersion, deviceName, err)
			// Ignore parsing errors, use default value 0
			deviceVersionStr = "0"
		} else {
			s.log.Infof("Successfully parsed device reported version '%s' to %d for device %s", *deviceReportedVersion, deviceVersion, deviceName)
			deviceVersionStr = *deviceReportedVersion
		}
	} else {
		s.log.Infof("No device reported version provided for device %s, using default 0", deviceName)
	}

	// Get service version from annotations
	var serviceVersion int64 = 0
	serviceVersionStr := "not found"
	if serviceVersionStrFromAnnotation, hasServiceVersion := annotations[api.DeviceAnnotationRenderedVersion]; hasServiceVersion {
		serviceVersionStr = serviceVersionStrFromAnnotation
		var err error
		serviceVersion, err = strconv.ParseInt(serviceVersionStrFromAnnotation, 10, 64)
		if err != nil {
			s.log.Warnf("Failed to parse service version '%s' for device %s: %v", serviceVersionStrFromAnnotation, deviceName, err)
			// Ignore parsing errors, use default value 0
		} else {
			s.log.Infof("Successfully parsed service version '%s' to %d for device %s", serviceVersionStrFromAnnotation, serviceVersion, deviceName)
		}
	} else {
		s.log.Infof("No service version annotation found for device %s, using default 0", deviceName)
	}

	// Track whether device will be moved to conflict paused state
	willBeConflictPaused := deviceVersion > serviceVersion
	s.log.Infof("Version comparison for device %s: deviceVersion=%d (from '%s'), serviceVersion=%d (from '%s'), willBeConflictPaused=%t",
		deviceName, deviceVersion, deviceVersionStr, serviceVersion, serviceVersionStr, willBeConflictPaused)

	// Generate detailed conflict paused info message
	var infoMessage string
	if willBeConflictPaused {
		deviceVersionDisplay := "unknown"
		if deviceReportedVersion != nil && *deviceReportedVersion != "" {
			deviceVersionDisplay = *deviceReportedVersion
		}
		infoMessage = fmt.Sprintf("Device reconciliation is paused due to a state conflict between the service and the device's agent; manual intervention is required. (device reported version %s > device version known to service %d)", deviceVersionDisplay, serviceVersion)
		s.log.Infof("Device %s will be moved to conflict paused state: %s", deviceName, infoMessage)
	} else {
		infoMessage = "Device is up to date"
		s.log.Infof("Device %s is up to date, will be set to online status", deviceName)
	}

	// Use raw SQL with parameterized queries to avoid JSON path syntax issues
	sql := `
		UPDATE devices 
		SET 
			annotations = (annotations - $1) || CASE 
				WHEN $2 THEN jsonb_build_object($3::text, 'true')
				ELSE '{}'::jsonb
			END,
			status = jsonb_set(
				jsonb_set(
					jsonb_set(COALESCE(status, '{}'::jsonb), '{summary}', jsonb_build_object('status', $4::text, 'info', $5::text), true),
					'{updated}', jsonb_build_object('status', $9::text), true
				),
				'{config,renderedVersion}', to_jsonb($8::text), true
			),
			resource_version = COALESCE(resource_version, 0) + 1
		WHERE org_id = $6 AND name = $7 AND deleted_at IS NULL
	`

	var status string
	if willBeConflictPaused {
		status = string(api.DeviceSummaryStatusConflictPaused)
	} else {
		status = string(api.DeviceSummaryStatusOnline)
	}

	// Determine updated status based on version comparison
	var updatedStatus string
	if deviceVersion == serviceVersion {
		updatedStatus = string(api.DeviceUpdatedStatusUpToDate)
	} else {
		updatedStatus = string(api.DeviceUpdatedStatusOutOfDate)
	}

	// Prepare the device reported version for the update
	deviceReportedVersionStr := "0"
	if deviceReportedVersion != nil && *deviceReportedVersion != "" {
		deviceReportedVersionStr = *deviceReportedVersion
	}

	s.log.Infof("Executing database update for device %s with status=%s, willBeConflictPaused=%t, deviceReportedVersionStr=%s, updatedStatus=%s",
		deviceName, status, willBeConflictPaused, deviceReportedVersionStr, updatedStatus)

	result = s.getDB(ctx).Exec(sql,
		api.DeviceAnnotationAwaitingReconnect,
		willBeConflictPaused,
		api.DeviceAnnotationConflictPaused,
		status,
		infoMessage,
		orgId,
		deviceName,
		deviceReportedVersionStr,
		updatedStatus,
	)
	err := ErrorFromGormError(result.Error)
	if err != nil {
		s.log.WithError(err).Errorf("Failed to update device %s in database", deviceName)
		return strings.Contains(err.Error(), "deadlock"), false, err
	}
	if result.RowsAffected == 0 {
		s.log.Warnf("No rows were updated for device %s - device may have been deleted or modified concurrently", deviceName)
		return true, false, flterrors.ErrNoRowsUpdated
	}

	s.log.Infof("Successfully updated device %s in database: rowsAffected=%d, willBeConflictPaused=%t",
		deviceName, result.RowsAffected, willBeConflictPaused)
	return false, willBeConflictPaused, nil
}

func (s *DeviceStore) setOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) (bool, error) {
	err := s.getDB(ctx).Model(&model.Device{}).Where("org_id = ? AND owner = ? AND (status->'updated'->>'status' = ?)",
		orgId, owner, api.DeviceUpdatedStatusUpToDate).Updates(map[string]any{
		"status": gorm.Expr(`jsonb_set(jsonb_set(status, '{updated,status}', to_jsonb(?::text)),'{updated,info}', to_jsonb(?::text))`,
			api.DeviceUpdatedStatusOutOfDate, api.DeviceOutOfSyncWithFleetText),
		"resource_version": gorm.Expr("resource_version + 1"),
	}).Error
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), ErrorFromGormError(err)
	}
	return false, nil
}

func (s *DeviceStore) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
	return retryUpdate(func() (bool, error) {
		return s.setOutOfDate(ctx, orgId, owner)
	})
}

func (s *DeviceStore) updateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string) (retry bool, renderedVersion string, err error) {
	existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.getDB(ctx).Take(&existingRecord)
	if result.Error != nil {
		return false, "", ErrorFromGormError(result.Error)
	}
	existingAnnotations := util.EnsureMap(existingRecord.Annotations)

	var deviceStatus *api.DeviceStatus
	if existingRecord.Status != nil {
		deviceStatus = &existingRecord.Status.Data
	}

	nextRenderedVersion, err := api.GetNextDeviceRenderedVersion(existingAnnotations, deviceStatus)
	if err != nil {
		return false, "", err
	}

	hash := specHash

	existingAnnotations[api.DeviceAnnotationRenderedVersion] = nextRenderedVersion
	if lo.HasKey(existingAnnotations, api.DeviceAnnotationTemplateVersion) {
		existingAnnotations[api.DeviceAnnotationRenderedTemplateVersion] = existingAnnotations[api.DeviceAnnotationTemplateVersion]
	}
	// Check if the rendered content has changed by comparing hashes
	if lo.HasKey(existingAnnotations, api.DeviceAnnotationRenderedSpecHash) {
		// if the hash is the same, we shouldn't update the rendered version
		if existingAnnotations[api.DeviceAnnotationRenderedSpecHash] == hash {
			return false, "", nil
		}
	}
	existingAnnotations[api.DeviceAnnotationRenderedSpecHash] = hash

	renderedApplicationsJSON := renderedApplications
	if strings.TrimSpace(renderedApplications) == "" {
		renderedApplicationsJSON = "[]"
	}

	result = s.getDB(ctx).Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":           model.MakeJSONMap(existingAnnotations),
		"rendered_config":       &renderedConfig,
		"rendered_applications": &renderedApplicationsJSON,
		"resource_version":      gorm.Expr("resource_version + 1"),
		"render_timestamp":      time.Now(),
	})

	err = ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), "", err
	}
	if result.RowsAffected == 0 {
		return true, "", flterrors.ErrNoRowsUpdated
	}
	return false, nextRenderedVersion, nil
}

func (s *DeviceStore) UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string) (string, error) {
	var rv string

	wrapper := func() (bool, error) {
		var retry bool
		var err error
		retry, rv, err = s.updateRendered(ctx, orgId, name, renderedConfig, renderedApplications, specHash)
		return retry, err
	}

	err := retryUpdate(wrapper)
	return rv, err
}

func (s *DeviceStore) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.Device, error) {
	deviceModel := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.getDB(ctx).Take(&deviceModel)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	return deviceModel.ToApiResource(model.WithRendered(knownRenderedVersion))
}

func (s *DeviceStore) GetLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*time.Time, error) {
	deviceModel := model.DeviceTimestamp{
		OrgID: orgId, Name: name,
	}
	result := s.getDB(ctx).Take(&deviceModel)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	return deviceModel.LastSeen, nil
}

func (s *DeviceStore) setServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition, callback ServiceConditionsCallback) (retry bool, err error) {
	existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.getDB(ctx).Take(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}

	// Capture old conditions with deep copy
	var oldConditions []api.Condition
	if existingRecord.ServiceConditions != nil && existingRecord.ServiceConditions.Data.Conditions != nil {
		// Deep copy the conditions to avoid shared memory issues
		oldConditions = append(oldConditions, *existingRecord.ServiceConditions.Data.Conditions...)
	}

	// Initialize service conditions if needed
	if existingRecord.ServiceConditions == nil {
		existingRecord.ServiceConditions = model.MakeJSONField(model.ServiceConditions{})
	}
	if existingRecord.ServiceConditions.Data.Conditions == nil {
		existingRecord.ServiceConditions.Data.Conditions = &[]api.Condition{}
	}

	// Set new conditions
	for _, condition := range conditions {
		api.SetStatusCondition(existingRecord.ServiceConditions.Data.Conditions, condition)
	}

	// Update using the original pattern with specific field updates and optimistic locking
	result = s.getDB(ctx).Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"service_conditions": existingRecord.ServiceConditions,
		"resource_version":   gorm.Expr("resource_version + 1"),
	})
	err = ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}

	// Call callback if provided (but don't fail the operation if callback fails)
	if callback != nil {
		// Convert the updated model to API resource for the callback
		apiDevice, convertErr := existingRecord.ToApiResource()
		if convertErr != nil {
			// Log the error but don't fail the operation
			s.log.Errorf("Failed to convert device to API resource for callback: %v", convertErr)
		} else {
			// Call callback in a defer with error recovery to prevent callback failures from affecting the main operation
			defer func() {
				if r := recover(); r != nil {
					s.log.Errorf("Callback panicked during service conditions update: %v", r)
				}
			}()

			// Call the callback - if it fails, log the error but don't propagate it
			func() {
				defer func() {
					if r := recover(); r != nil {
						s.log.Errorf("Service conditions callback panicked: %v", r)
					}
				}()
				callback(ctx, orgId, apiDevice, oldConditions, *existingRecord.ServiceConditions.Data.Conditions)
			}()
		}
	}

	return false, nil
}

func (s *DeviceStore) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition, callback ServiceConditionsCallback) error {
	return retryUpdate(func() (bool, error) {
		return s.setServiceConditions(ctx, orgId, name, conditions, callback)
	})
}

func (s *DeviceStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	repos := []model.Repository{}
	for _, repoName := range repositoryNames {
		repos = append(repos, model.Repository{Resource: model.Resource{OrgID: orgId, Name: repoName}})
	}
	return s.getDB(ctx).Transaction(func(innerTx *gorm.DB) error {
		device := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		if err := innerTx.Model(&device).Association("Repositories").Replace(repos); err != nil {
			return ErrorFromGormError(err)
		}
		return nil
	})
}

func (s *DeviceStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error) {
	device := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
	var repos []model.Repository
	err := s.getDB(ctx).Model(&device).Association("Repositories").Find(&repos)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	repositories, err := model.RepositoriesToApiResource(repos, nil, nil)
	if err != nil {
		return nil, err
	}
	return &repositories, nil
}

func (s *DeviceStore) ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams ListParams) (*api.DeviceList, error) {
	// Use raw SQL to efficiently query JSONB service_conditions field
	// The service_conditions field is stored as JSONB for optimal performance
	var devices []model.Device
	var nextContinue *string
	var numRemaining *int64

	// Build the raw SQL query with proper pagination support for JSONB
	baseSQL := `
		SELECT * FROM devices
		WHERE org_id = ?
			AND deleted_at IS NULL
			AND service_conditions IS NOT NULL
			AND EXISTS (
				SELECT 1 FROM jsonb_array_elements(service_conditions->'conditions') AS elem
				WHERE elem->>'type' = ? AND elem->>'status' = ?
			)`

	// Handle pagination - add WHERE condition before ORDER BY
	var args []interface{}
	args = append(args, orgId, conditionType, conditionStatus)

	if listParams.Continue != nil && len(listParams.Continue.Names) > 0 {
		baseSQL += " AND name > ?"
		args = append(args, listParams.Continue.Names[0])
	}

	// Add ORDER BY after all WHERE conditions
	baseSQL += " ORDER BY name ASC"

	// Add limit
	baseSQL += " LIMIT ?"
	args = append(args, listParams.Limit)

	// Execute the query
	if err := s.getDB(ctx).Raw(baseSQL, args...).Scan(&devices).Error; err != nil {
		return nil, ErrorFromGormError(err)
	}

	// Calculate pagination metadata
	if len(devices) > 0 && len(devices) == listParams.Limit {
		// Check if there are more results
		var count int64
		countSQL := `
			SELECT COUNT(*) FROM devices
			WHERE org_id = ?
				AND deleted_at IS NULL
				AND service_conditions IS NOT NULL
				AND EXISTS (
					SELECT 1 FROM jsonb_array_elements(service_conditions->'conditions') AS elem
					WHERE elem->>'type' = ? AND elem->>'status' = ?
				) AND name > ?`

		countArgs := []interface{}{orgId, conditionType, conditionStatus, devices[len(devices)-1].Name}
		if err := s.getDB(ctx).Raw(countSQL, countArgs...).Scan(&count).Error; err != nil {
			return nil, ErrorFromGormError(err)
		}

		if count > 0 {
			nextContinue = BuildContinueString([]string{devices[len(devices)-1].Name}, count)
			numRemaining = &count
		}
	}

	result, err := model.DevicesToApiResource(devices, nextContinue, numRemaining)
	return &result, err
}

// CountByOrgAndStatusResult holds the result of the group by query
// for organization and status.
type CountByOrgAndStatusResult struct {
	OrgID  string
	Status string
	Fleet  string
	Count  int64
}

// CountByOrgAndStatus returns the count of devices grouped by org_id and status.
func (s *DeviceStore) CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, statusType DeviceStatusType, groupByFleet bool) ([]CountByOrgAndStatusResult, error) {
	var query *gorm.DB
	var err error

	if orgId != nil {
		query, err = ListQuery(&model.Device{}).BuildNoOrder(ctx, s.getDB(ctx), *orgId, ListParams{})
	} else {
		// When orgId is nil, we don't filter by org_id
		query = s.getDB(ctx).Model(&model.Device{})
	}

	if err != nil {
		return nil, err
	}

	// Validate the status type
	if err := statusType.Validate(); err != nil {
		return nil, err
	}

	// Determine which status field to use
	var statusField string
	switch statusType {
	case DeviceStatusTypeSummary:
		statusField = "status->'summary'->>'status'"
	case DeviceStatusTypeApplication:
		statusField = "status->'applicationsSummary'->>'status'"
	case DeviceStatusTypeUpdate:
		statusField = "status->'updated'->>'status'"
	default:
		statusField = "status->'summary'->>'status'" // default to summary
	}

	selectList := []string{
		"org_id as org_id",
		statusField + " as status",
		"COUNT(*) as count",
	}

	if groupByFleet {
		selectList = append(selectList, "owner as fleet")
	}
	groupList := []string{"org_id", statusField}
	if groupByFleet {
		groupList = append(groupList, "owner")
	}
	query = query.Select(selectList).Group(strings.Join(groupList, ","))

	var results []CountByOrgAndStatusResult
	err = query.Scan(&results).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	return results, nil
}

// PrepareDevicesAfterRestore sets the waitingForConnectionAfterRestore annotation
// on all devices, clears their lastSeen timestamps, and sets status summary using efficient SQL
func (s *DeviceStore) PrepareDevicesAfterRestore(ctx context.Context) (int64, error) {
	db := s.getDB(ctx)

	// Use raw SQL for efficient bulk update that preserves existing annotations
	// and properly handles the status JSON structure using || operator for merging
	sql := `
        WITH updated_devices AS (
		UPDATE devices 
		SET 
			annotations = COALESCE(annotations, '{}'::jsonb) || jsonb_build_object($1::text, 'true'),
			status = CASE 
				WHEN status IS NOT NULL THEN 
					status || jsonb_build_object('summary', jsonb_build_object('status', $2::text, 'info', $3::text)) || jsonb_build_object('updated', jsonb_build_object('status', $6::text))
				ELSE 
					jsonb_build_object('summary', jsonb_build_object('status', $2::text, 'info', $3::text)) || jsonb_build_object('updated', jsonb_build_object('status', $6::text))
			END,
			resource_version = COALESCE(resource_version, 0) + 1
		WHERE deleted_at IS NULL 
			AND NOT (status->'lifecycle'->>'status') IN ($4, $5)
			AND (annotations->>$1) IS DISTINCT FROM 'true'
        RETURNING name, org_id)
        UPDATE device_timestamps dt
        SET last_seen = NULL
		FROM updated_devices ud
		WHERE dt.org_id = ud.org_id AND dt.name = ud.name
	`

	result := db.Exec(sql,
		api.DeviceAnnotationAwaitingReconnect,
		api.DeviceSummaryStatusAwaitingReconnect,
		"Device has not reconnected since restore to confirm its current state.",
		api.DeviceLifecycleStatusDecommissioned,
		api.DeviceLifecycleStatusDecommissioning,
		api.DeviceUpdatedStatusUnknown,
	)

	if result.Error != nil {
		return 0, ErrorFromGormError(result.Error)
	}

	return result.RowsAffected, nil
}

// RemoveConflictPausedAnnotation removes the conflictPaused annotation from all devices matching the selector
// Returns the count of affected devices and their IDs
func (s *DeviceStore) RemoveConflictPausedAnnotation(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, []string, error) {
	var affectedRows int64
	var deviceIDs []string

	err := retryUpdate(func() (bool, error) {
		// Use RETURNING clause to get the names of actually updated devices
		var updatedDevices []model.Device

		query, err := ListQuery(&updatedDevices).BuildNoOrder(ctx, s.getDB(ctx), orgId, listParams)
		if err != nil {
			return false, err
		}

		// Only update devices that actually have the conflictPaused annotation
		query = query.Where("annotations ? ?", gorm.Expr("?"), api.DeviceAnnotationConflictPaused)

		result := query.
			Clauses(clause.Returning{}).
			Updates(map[string]any{
				"annotations":      gorm.Expr("annotations - ? - ?", api.DeviceAnnotationConflictPaused, api.DeviceAnnotationRenderedSpecHash),
				"resource_version": gorm.Expr("resource_version + 1"),
			})

		affectedRows = result.RowsAffected
		err = ErrorFromGormError(result.Error)
		if err != nil {
			return strings.Contains(err.Error(), "deadlock"), err
		}

		// Extract device names from the returned devices
		deviceIDs = make([]string, len(updatedDevices))
		for i, device := range updatedDevices {
			deviceIDs[i] = device.Name
		}

		return false, nil
	})

	return affectedRows, deviceIDs, err
}

// GetAllDeviceNames returns all device names for a given organization
// This is used for restoration to add awaiting reconnection keys
func (s *DeviceStore) GetAllDeviceNames(ctx context.Context, orgId uuid.UUID) ([]string, error) {
	var deviceNames []string

	// Use raw SQL for efficiency - we only need the device names
	err := s.getDB(ctx).Model(&model.Device{}).
		Where("org_id = ? AND deleted_at IS NULL", orgId).
		Pluck("name", &deviceNames).Error

	if err != nil {
		return nil, ErrorFromGormError(err)
	}

	return deviceNames, nil
}
