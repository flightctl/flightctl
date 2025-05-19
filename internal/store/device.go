package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Device interface {
	InitialMigration() error

	// Exposed to users
	Create(ctx context.Context, orgId uuid.UUID, device *api.Device, callback DeviceStoreCallback) (*api.Device, error)
	Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, callback DeviceStoreCallback) (*api.Device, api.ResourceUpdatedDetails, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, callback DeviceStoreCallback) (*api.Device, bool, api.ResourceUpdatedDetails, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error)
	Labels(ctx context.Context, orgId uuid.UUID, listParams ListParams) (api.LabelList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error)
	GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.Device, error)

	// Used internally
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications string) error
	SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error)

	// Used only by rollout
	Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error)
	UnmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) error
	MarkRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams ListParams, limit *int) error
	CompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, error)
	CountByLabels(ctx context.Context, orgId uuid.UUID, listParams ListParams, groupBy []string) ([]map[string]any, error)
	Summary(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DevicesSummary, error)

	// Used only by device_disconnected
	UpdateSummaryStatusBatch(ctx context.Context, orgId uuid.UUID, deviceNames []string, status api.DeviceSummaryStatusType, statusInfo string) error

	// Used by tests
	SetIntegrationTestCreateOrUpdateCallback(IntegrationTestCallback)
}

type DeviceStore struct {
	db           *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.Device, model.Device, api.Device, api.DeviceList]
}

type DeviceStoreCallback func(orgId uuid.UUID, before *api.Device, after *api.Device)
type DeviceStoreValidationCallback func(before *api.Device, after *api.Device) error
type DeviceStoreAllDeletedCallback func(orgId uuid.UUID)

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
	return &DeviceStore{db: db, log: log, genericStore: genericStore}
}

func (s *DeviceStore) SetIntegrationTestCreateOrUpdateCallback(c IntegrationTestCallback) {
	s.genericStore.IntegrationTestCreateOrUpdateCallback = c
}

func (s *DeviceStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.Device{}, &model.DeviceLabel{}); err != nil {
		return err
	}

	// Create index for device primary key 'name'
	if !s.db.Migrator().HasIndex(&model.Device{}, "idx_device_primary_key_name") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_device_primary_key_name ON devices USING BTREE (name)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Device{}, "PrimaryKeyColumnName"); err != nil {
				return err
			}
		}
	}

	// Create indexes for device 'Alias' column
	if !s.db.Migrator().HasIndex(&model.Device{}, "device_alias") {
		if s.db.Dialector.Name() == "postgres" {
			// Enable pg_trgm extension if not already enabled
			if err := s.db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
				return err
			}
			// Create a B-Tree index for exact matches on the 'Alias' field
			if err := s.db.Exec("CREATE INDEX IF NOT EXISTS device_alias_btree ON devices USING BTREE (alias)").Error; err != nil {
				return err
			}
			// Create a GIN index for substring matches on the 'Alias' field
			if err := s.db.Exec("CREATE INDEX IF NOT EXISTS device_alias_gin ON devices USING GIN (alias gin_trgm_ops)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Device{}, "device_alias"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for device labels
	if !s.db.Migrator().HasIndex(&model.Device{}, "idx_device_labels") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_device_labels ON devices USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Device{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for device annotations
	if !s.db.Migrator().HasIndex(&model.Device{}, "idx_device_annotations") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_device_annotations ON devices USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Device{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for device status
	if !s.db.Migrator().HasIndex(&model.Device{}, "idx_device_status") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_device_status ON devices USING GIN (status)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Device{}, "Status"); err != nil {
				return err
			}
		}
	}

	// Create indexes for device_labels (Partial Matching Support)
	if !s.db.Migrator().HasIndex(&model.DeviceLabel{}, "idx_device_labels_partial") {
		if s.db.Dialector.Name() == "postgres" {
			// Enable pg_trgm extension for partial matching
			if err := s.db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
				return err
			}
			// Create GIN index for partial match searches
			if err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_device_labels_partial ON device_labels USING GIN (label_key gin_trgm_ops, label_value gin_trgm_ops)").Error; err != nil {
				return err
			}
		}
	}

	// Ensure trigger is created for INSERT & UPDATE (labels JSONB changes)
	if s.db.Dialector.Name() == "postgres" {
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
		if err := s.db.Exec(triggerSQL).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *DeviceStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Device, callback DeviceStoreCallback) (*api.Device, error) {
	return s.genericStore.Create(ctx, orgId, resource, callback)
}

func (s *DeviceStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, callback DeviceStoreCallback) (*api.Device, api.ResourceUpdatedDetails, error) {
	return s.genericStore.Update(ctx, orgId, resource, fieldsToUnset, fromAPI, validationCallback, callback)
}

func (s *DeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, callback DeviceStoreCallback) (*api.Device, bool, api.ResourceUpdatedDetails, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, validationCallback, callback)
}

func (s *DeviceStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *DeviceStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error) {
	return s.genericStore.List(ctx, orgId, listParams, nil)
}

func (s *DeviceStore) Labels(ctx context.Context, orgId uuid.UUID, listParams ListParams) (api.LabelList, error) {
	var labels []model.DeviceLabel

	resolver, err := selector.NewCompositeSelectorResolver(&model.DeviceLabel{}, &model.Device{})
	if err != nil {
		return nil, fmt.Errorf("failed to create selector resolver: %w", err)
	}

	query, err := ListQuery(model.Device{}, WithSelectorResolver(resolver)).BuildNoOrder(ctx, s.db, orgId, listParams)
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

func (s *DeviceStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error {
	return s.genericStore.Delete(
		ctx,
		model.Device{Resource: model.Resource{OrgID: orgId, Name: name}},
		callback,
		Resource{Table: "enrollment_requests", OrgID: orgId.String(), Name: name})
}

func (s *DeviceStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error {
	return s.genericStore.DeleteAll(ctx, orgId, callback)
}

func (s *DeviceStore) Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error) {
	query, err := ListQuery(&model.Device{}).Build(ctx, s.db, orgId, listParams)
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
	err := s.db.Raw(fmt.Sprintf(`select count(*) as count, 
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
	err := s.db.Model(&model.Device{}).Where("org_id = ? and owner = ? and annotations ? ?",
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
	query, err := ListQuery(&model.Device{}).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return false, err
	}
	if limit != nil {
		query = query.Limit(*limit)
		query = s.db.Model(&model.Device{}).Where("org_id = ? and name in (?)", orgId,
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
	query, err := ListQuery(&model.Device{}).BuildNoOrder(ctx, s.db, orgId, listParams)
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
	args = append(args, gorm.Expr("status -> 'summary' ->> 'status' <> 'Unknown' and status -> 'config' ->> 'renderedVersion' <> COALESCE(annotations ->> ?, '')",
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
	query, err := ListQuery(&model.Device{}).Build(ctx, s.db, orgId, listParams)
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

func (s *DeviceStore) UpdateSummaryStatusBatch(ctx context.Context, orgId uuid.UUID, deviceNames []string, status api.DeviceSummaryStatusType, statusInfo string) error {
	if len(deviceNames) == 0 {
		return nil
	}

	tokens := strings.Repeat("?,", len(deviceNames))
	// trim tailing comma
	tokens = tokens[:len(tokens)-1]

	// https://www.postgresql.org/docs/current/functions-json.html
	// jsonb_set(target jsonb, path text[], new_value jsonb, create_missing boolean)
	createMissing := "false"
	query := fmt.Sprintf(`
        UPDATE devices
        SET 
            status = jsonb_set(
                jsonb_set(status, '{summary,status}', '"%s"', %s), 
                '{summary,info}', '"%s"'
            ),
            resource_version = resource_version + 1
        WHERE name IN (%s)`, status, createMissing, statusInfo, tokens)

	args := make([]interface{}, len(deviceNames))
	for i, name := range deviceNames {
		args[i] = name
	}

	return s.db.WithContext(ctx).Exec(query, args...).Error
}

func (s *DeviceStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Device) (*api.Device, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}

func (s *DeviceStore) updateAnnotations(orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) (bool, error) {
	existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.db.First(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	existingAnnotations := util.EnsureMap(existingRecord.Annotations)

	existingConsoleAnnotation := util.DefaultIfNotInMap(existingAnnotations, api.DeviceAnnotationConsole, "")
	existingAnnotations = util.MergeLabels(existingAnnotations, annotations)

	for _, deleteKey := range deleteKeys {
		delete(existingAnnotations, deleteKey)
	}
	newConsoleAnnotation := util.DefaultIfNotInMap(existingAnnotations, api.DeviceAnnotationConsole, "")

	// Changing the console annotation requires bumping the renderedVersion annotation
	if existingConsoleAnnotation != newConsoleAnnotation {
		nextRenderedVersion, err := api.GetNextDeviceRenderedVersion(existingAnnotations)
		if err != nil {
			return false, err
		}

		existingAnnotations[api.DeviceAnnotationRenderedVersion] = nextRenderedVersion
	}

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
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
		return s.updateAnnotations(orgId, name, annotations, deleteKeys)
	})
}

func (s *DeviceStore) updateRendered(orgId uuid.UUID, name, renderedConfig, renderedApplications string) (retry bool, err error) {
	existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.db.First(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	existingAnnotations := util.EnsureMap(existingRecord.Annotations)

	nextRenderedVersion, err := api.GetNextDeviceRenderedVersion(existingAnnotations)
	if err != nil {
		return false, err
	}

	existingAnnotations[api.DeviceAnnotationRenderedVersion] = nextRenderedVersion
	if lo.HasKey(existingAnnotations, api.DeviceAnnotationTemplateVersion) {
		existingAnnotations[api.DeviceAnnotationRenderedTemplateVersion] = existingAnnotations[api.DeviceAnnotationTemplateVersion]
	}

	renderedApplicationsJSON := renderedApplications
	if strings.TrimSpace(renderedApplications) == "" {
		renderedApplicationsJSON = "[]"
	}

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":           model.MakeJSONMap(existingAnnotations),
		"rendered_config":       &renderedConfig,
		"rendered_applications": &renderedApplicationsJSON,
		"resource_version":      gorm.Expr("resource_version + 1"),
		"render_timestamp":      time.Now(),
	})

	err = ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *DeviceStore) UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications string) error {
	return retryUpdate(func() (bool, error) {
		return s.updateRendered(orgId, name, renderedConfig, renderedApplications)
	})
}

func (s *DeviceStore) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.Device, error) {
	deviceModel := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&deviceModel)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	return deviceModel.ToApiResource(model.WithRendered(knownRenderedVersion))
}

func (s *DeviceStore) setServiceConditions(orgId uuid.UUID, name string, conditions []api.Condition) (retry bool, err error) {
	existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.db.First(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}

	if existingRecord.ServiceConditions == nil {
		existingRecord.ServiceConditions = model.MakeJSONField(model.ServiceConditions{})
	}
	if existingRecord.ServiceConditions.Data.Conditions == nil {
		existingRecord.ServiceConditions.Data.Conditions = &[]api.Condition{}
	}

	for _, condition := range conditions {
		api.SetStatusCondition(existingRecord.ServiceConditions.Data.Conditions, condition)
	}

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
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
	return false, nil
}

func (s *DeviceStore) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error {
	return retryUpdate(func() (bool, error) {
		return s.setServiceConditions(orgId, name, conditions)
	})
}

func (s *DeviceStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	repos := []model.Repository{}
	for _, repoName := range repositoryNames {
		repos = append(repos, model.Repository{Resource: model.Resource{OrgID: orgId, Name: repoName}})
	}
	return s.db.Transaction(func(innerTx *gorm.DB) error {
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
	err := s.db.Model(&device).Association("Repositories").Find(&repos)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	repositories, err := model.RepositoriesToApiResource(repos, nil, nil)
	if err != nil {
		return nil, err
	}
	return &repositories, nil
}
