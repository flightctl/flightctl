package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Device interface {
	Create(ctx context.Context, orgId uuid.UUID, device *api.Device, callback DeviceStoreCallback) (*api.Device, error)
	Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, callback DeviceStoreCallback) (*api.Device, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error)
	Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error)
	UnmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) error
	MarkRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams ListParams, limit *int) error
	CompletionCounts(ctx context.Context, orgId uuid.UUID, owner string) ([]CompletionCount, error)
	CountByLabels(ctx context.Context, orgId uuid.UUID, listParams ListParams, groupBy []string, modifiers ...queryModifier) ([]map[string]any, error)
	Summary(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DevicesSummary, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, callback DeviceStoreCallback) (*api.Device, bool, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error)
	UpdateSummaryStatusBatch(ctx context.Context, orgId uuid.UUID, deviceNames []string, status api.DeviceSummaryStatusType, statusInfo string) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications string) error
	GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.RenderedDeviceSpec, error)
	SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error)
	InitialMigration() error
	SetIntegrationTestCreateOrUpdateCallback(IntegrationTestCallback)
}

type IntegrationTestCallback func()
type DeviceStore struct {
	db  *gorm.DB
	log logrus.FieldLogger

	IntegrationTestCreateOrUpdateCallback IntegrationTestCallback
}

type DeviceStoreCallback func(before *model.Device, after *model.Device)
type DeviceStoreAllDeletedCallback func(orgId uuid.UUID)

// Make sure we conform to Device interface
var _ Device = (*DeviceStore)(nil)

func NewDevice(db *gorm.DB, log logrus.FieldLogger) Device {
	return &DeviceStore{db: db, log: log, IntegrationTestCreateOrUpdateCallback: func() {}}
}

func (s *DeviceStore) SetIntegrationTestCreateOrUpdateCallback(c IntegrationTestCallback) {
	s.IntegrationTestCreateOrUpdateCallback = c
}

func (s *DeviceStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.Device{}); err != nil {
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

	// TODO: generalize this for fleet, enrollmentrequest, etc. Make part of the base resource
	if !s.db.Migrator().HasIndex(&model.Device{}, "device_labels") {
		// see https://github.com/go-gorm/gorm/discussions/6695
		if s.db.Dialector.Name() == "postgres" {
			// GiST index could also be used: https://www.postgresql.org/docs/9.1/textsearch-indexes.html
			if err := s.db.Exec("CREATE INDEX device_labels ON devices USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Device{}, "Labels"); err != nil {
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

	return nil
}

func (s *DeviceStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Device, callback DeviceStoreCallback) (*api.Device, error) {
	updatedResource, _, _, err := s.createOrUpdate(orgId, resource, nil, true, ModeCreateOnly, callback)
	return updatedResource, err
}

func (s *DeviceStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, callback DeviceStoreCallback) (*api.Device, error) {
	updatedResource, _, err := retryCreateOrUpdate(func() (*api.Device, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, fieldsToUnset, fromAPI, ModeUpdateOnly, callback)
	})
	return updatedResource, err
}

func (s *DeviceStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error) {
	var devices model.DeviceList
	var nextContinue *string
	var numRemaining *int64

	if listParams.Limit < 0 {
		return nil, flterrors.ErrLimitParamOutOfBounds
	}

	query, err := ListQuery(&model.Device{}).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	result := query.Find(&devices)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(devices) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    devices[len(devices)-1].Name,
			Version: CurrentContinueVersion,
		}
		devices = devices[:len(devices)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery, err := ListQuery(&model.Device{}).Build(ctx, s.db, orgId, listParams)
			if err != nil {
				return nil, err
			}
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiDevicelist := devices.ToApiResource(nextContinue, numRemaining)
	return &apiDevicelist, ErrorFromGormError(result.Error)
}

func (s *DeviceStore) Count(ctx context.Context, orgId uuid.UUID, listParams ListParams) (int64, error) {
	query, err := ListQuery(&model.DeviceList{}).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return 0, err
	}
	var devicesCount int64
	if err := query.Count(&devicesCount).Error; err != nil {
		return 0, ErrorFromGormError(err)
	}
	return devicesCount, nil
}

type CompletionCount struct {
	Count                   int64
	SameRenderedVersion     bool
	RenderedTemplateVersion string
	UpdatingReason          string
	SummaryStatus           string
}

// CompletionCounts is used for finding if a rollout batch is complete or to set the success percentage of the batch.
// The result is a count of devices grouped by some fields:
// - rendered_template_version: taken from the annotation 'device-controller/renderedTemplateVersion'
// - summary_status: taken from the field 'status.summary.status'
// - updating_reason: it is the reason field from a condition having type 'Updating'
// - same_rendered_version: it is the result of comparison for equality between the annotation 'device-controller/renderedVersion' and the field 'status.config.renderedVersion'
func (s *DeviceStore) CompletionCounts(ctx context.Context, orgId uuid.UUID, owner string) ([]CompletionCount, error) {
	var results []CompletionCount
	err := s.db.Raw(fmt.Sprintf(`select count(*) as count, 
                                 status -> 'config' ->> 'renderedVersion' = rendered_version as same_rendered_version, 
                                 elem ->> 'reason' as updating_reason,
                                 status -> 'summary' ->> 'status' as summary_status,
                                 rendered_template_version
                          from devices d LEFT JOIN LATERAL (
                            SELECT elem
						    FROM jsonb_array_elements(d.status->'conditions') AS elem
						    WHERE elem->>'type' = 'Updating'
						    LIMIT 1
							) subquery ON TRUE 
							LEFT JOIN LATERAL (
                                 SELECT substr(ann, length('%s=')+1) AS rendered_template_version
                                  FROM UNNEST(annotations) AS ann
                                   WHERE substr(ann, 1, length('%s=')) = '%s='
                                   LIMIT 1) AS tv_tab ON true
							LEFT JOIN LATERAL (
                                 SELECT substr(ann, length('%s=')+1) AS rendered_version
                                  FROM UNNEST(annotations) AS ann
                                   WHERE substr(ann, 1, length('%s=')) = '%s='
                                   LIMIT 1) AS rv_tab ON true
						     where
						       selected_for_rollout = true and org_id = ? and owner = ? group by same_rendered_version, updating_reason, summary_status, rendered_template_version`,
		api.DeviceAnnotationRenderedTemplateVersion, api.DeviceAnnotationRenderedTemplateVersion, api.DeviceAnnotationRenderedTemplateVersion,
		api.DeviceAnnotationRenderedVersion, api.DeviceAnnotationRenderedVersion, api.DeviceAnnotationRenderedVersion), orgId, owner).Scan(&results).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	return results, nil
}

// UnmarkRolloutSelection unmarks all previously marked devices for rollout in a fleet
func (s *DeviceStore) UnmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) error {
	err := s.db.Model(&model.Device{}).Where("org_id = ? and owner = ? and selected_for_rollout is not null", orgId, fmt.Sprintf("%s/%s", model.FleetKind, fleetName)).Update("selected_for_rollout", nil).Error
	return ErrorFromGormError(err)
}

// MarkRolloutSelection marks all devices that can be filtered by the list params.  If limit is provided then the number of marked devices
// will not be greater than the provided limit.
func (s *DeviceStore) MarkRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams ListParams, limit *int) error {
	query, err := ListQuery(&model.Device{}).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return err
	}
	if limit != nil {
		query = query.Limit(*limit)
	}
	err = s.db.Model(&model.Device{}).Where("org_id = ? and name in (?)", orgId,
		query.Select("name")).Update("selected_for_rollout", true).Error
	return ErrorFromGormError(err)
}

func labelKeyToSymbol(labelKey string) string {
	return strings.Replace(strings.Replace(labelKey, "-", "_dash_", -1), "/", "_slash_", -1)
}

// createLabelJoins is a helper function to add labels as selected columns that can be used for selection and grouping
// by the function CountByLabels.  Every join sub-statement is used to represent a single label.
func createLabelJoins(query *gorm.DB, groupBy []string) *gorm.DB {
	format := `LEFT JOIN LATERAL (
    SELECT substr(label, length('%s=')+1) AS %s
    FROM UNNEST(labels) AS label
    WHERE substr(label, 1, length('%s') + 1) = '%s='
    LIMIT 1) AS %s_tab ON true`
	return query.Joins(strings.Join(lo.Map(groupBy, func(labelKey string, _ int) string {
		labelSymbol := labelKeyToSymbol(labelKey)
		return fmt.Sprintf(format, labelKey, labelSymbol, labelKey, labelKey, labelSymbol)
	}), " "))
}

// CountByLabels is used for rollout policy disruption allowance to provide device count values grouped by the label values.
func (s *DeviceStore) CountByLabels(ctx context.Context, orgId uuid.UUID, listParams ListParams, groupBy []string, modifiers ...queryModifier) ([]map[string]any, error) {
	query, err := ListQuery(&model.DeviceList{}).BuildNoOrder(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}
	for _, m := range modifiers {
		query = m(query)
	}
	labelSymbols := lo.Map(groupBy, func(s string, _ int) string { return labelKeyToSymbol(s) })
	selectList := append(labelSymbols, "count(*) as count")
	query.Select(strings.Join(selectList, ","))
	for _, g := range labelSymbols {
		query = query.Group(g)
	}
	if len(groupBy) > 0 {
		query = createLabelJoins(query, groupBy)
	}
	var results []map[string]any
	err = query.Scan(&results).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	ret := lo.Map(results, func(m map[string]any, _ int) map[string]any {
		return lo.SliceToMap(append(groupBy, "count"), func(s string) (string, any) {
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

func (s *DeviceStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error {
	condition := model.Device{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)

	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	callback(orgId)

	return nil
}

func (s *DeviceStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&device)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	apiDevice := device.ToApiResource()
	return &apiDevice, nil
}

func (s *DeviceStore) createDevice(device *model.Device) (bool, error) {
	device.Generation = lo.ToPtr[int64](1)
	device.ResourceVersion = lo.ToPtr[int64](1)
	if result := s.db.Create(device); result.Error != nil {
		err := ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *DeviceStore) updateDevice(fromAPI bool, existingRecord, device *model.Device, fieldsToUnset []string) (bool, error) {
	sameSpec := api.DeviceSpecsAreEqual(device.Spec.Data, existingRecord.Spec.Data)

	// Update the generation if the spec was updated
	if !sameSpec {
		if fromAPI {
			if len(lo.FromPtr(existingRecord.Owner)) != 0 {
				// Don't let the user update the device spec if it's part of a fleet
				return false, flterrors.ErrUpdatingResourceWithOwnerNotAllowed
			} else {
				// If the device isn't part of a fleet, make sure it doesn't have the TV annotation
				existingAnnotations := util.LabelArrayToMap(existingRecord.Annotations)
				if existingAnnotations[api.DeviceAnnotationTemplateVersion] != "" {
					delete(existingAnnotations, api.DeviceAnnotationTemplateVersion)
					annotationsArray := util.LabelMapToArray(&existingAnnotations)
					device.Annotations = pq.StringArray(annotationsArray)
				}
			}
		}

		device.Generation = lo.ToPtr(lo.FromPtr(existingRecord.Generation) + 1)
	}
	if device.ResourceVersion != nil && lo.FromPtr(existingRecord.ResourceVersion) != lo.FromPtr(device.ResourceVersion) {
		return false, flterrors.ErrResourceVersionConflict
	}
	device.ResourceVersion = lo.ToPtr(lo.FromPtr(existingRecord.ResourceVersion) + 1)
	where := model.Device{Resource: model.Resource{OrgID: device.OrgID, Name: device.Name}}
	query := s.db.Model(where).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion))

	selectFields := []string{"spec", "alias"}
	selectFields = append(selectFields, GetNonNilFieldsFromResource(device.Resource)...)
	selectFields = append(selectFields, fieldsToUnset...)
	query = query.Select(selectFields)
	result := query.Updates(&device)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *DeviceStore) createOrUpdate(orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, mode CreateOrUpdateMode, callback DeviceStoreCallback) (*api.Device, bool, bool, error) {
	if resource == nil {
		return nil, false, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, false, flterrors.ErrResourceNameIsNil
	}

	device, err := model.NewDeviceFromApiResource(resource)
	if err != nil {
		return nil, false, false, err
	}
	device.OrgID = orgId

	// Use the dedicated API to update annotations
	device.Annotations = nil

	existingRecord, err := getExistingRecord[model.Device](s.db, device.Name, orgId)
	if err != nil {
		return nil, false, false, err
	}
	exists := existingRecord != nil

	if exists && mode == ModeCreateOnly {
		return nil, false, false, flterrors.ErrDuplicateName
	}
	if !exists && mode == ModeUpdateOnly {
		return nil, false, false, flterrors.ErrResourceNotFound
	}

	s.IntegrationTestCreateOrUpdateCallback()
	if !exists {
		if retry, err := s.createDevice(device); err != nil {
			return nil, false, retry, err
		}
	} else {
		if retry, err := s.updateDevice(fromAPI, existingRecord, device, fieldsToUnset); err != nil {
			return nil, false, retry, err
		}
	}

	callback(existingRecord, device)

	updatedResource := device.ToApiResource()
	return &updatedResource, !exists, false, nil
}

func (s *DeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, callback DeviceStoreCallback) (*api.Device, bool, error) {
	return retryCreateOrUpdate(func() (*api.Device, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, fieldsToUnset, fromAPI, ModeCreateOrUpdate, callback)
	})
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
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.Model(&device).Updates(map[string]interface{}{
		"status":           model.MakeJSONField(resource.Status),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	return resource, ErrorFromGormError(result.Error)
}

func (s *DeviceStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error {
	var existingRecord model.Device
	log := log.WithReqIDFromCtx(ctx, s.log)
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return ErrorFromGormError(result.Error)
		}

		associatedRecord := model.EnrollmentRequest{Resource: model.Resource{OrgID: orgId, Name: name}}

		if err := innerTx.Unscoped().Delete(&existingRecord).Error; err != nil {
			return ErrorFromGormError(err)
		}

		if err := innerTx.Unscoped().Delete(&associatedRecord).Error; err != nil {
			log.Warningf("failed to delete associated enrollment request: %v", err)
		}

		return nil
	})

	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil
		}
		return err
	}

	callback(&existingRecord, nil)
	return nil
}

func (s *DeviceStore) updateAnnotations(orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) (bool, error) {
	existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.db.First(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	existingAnnotations := util.LabelArrayToMap(existingRecord.Annotations)

	existingConsoleAnnotation := util.DefaultIfNotInMap(existingAnnotations, api.DeviceAnnotationConsole, "")
	existingAnnotations = util.MergeLabels(existingAnnotations, annotations)

	for _, deleteKey := range deleteKeys {
		delete(existingAnnotations, deleteKey)
	}
	newConsoleAnnotation := util.DefaultIfNotInMap(existingAnnotations, api.DeviceAnnotationConsole, "")

	// Changing the console annotation requires bumping the renderedVersion annotation
	if existingConsoleAnnotation != newConsoleAnnotation {
		nextRenderedVersion, err := getNextRenderedVersion(existingAnnotations)
		if err != nil {
			return false, err
		}

		existingAnnotations[api.DeviceAnnotationRenderedVersion] = nextRenderedVersion
	}

	annotationsArray := util.LabelMapToArray(&existingAnnotations)

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":      pq.StringArray(annotationsArray),
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
	existingAnnotations := util.LabelArrayToMap(existingRecord.Annotations)

	nextRenderedVersion, err := getNextRenderedVersion(existingAnnotations)
	if err != nil {
		return false, err
	}

	existingAnnotations[api.DeviceAnnotationRenderedVersion] = nextRenderedVersion
	if lo.HasKey(existingAnnotations, api.DeviceAnnotationTemplateVersion) {
		existingAnnotations[api.DeviceAnnotationRenderedTemplateVersion] = existingAnnotations[api.DeviceAnnotationTemplateVersion]
	}
	annotationsArray := util.LabelMapToArray(&existingAnnotations)

	renderedApplicationsJSON := renderedApplications
	if strings.TrimSpace(renderedApplications) == "" {
		renderedApplicationsJSON = "[]"
	}

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":           pq.StringArray(annotationsArray),
		"rendered_config":       &renderedConfig,
		"rendered_applications": &renderedApplicationsJSON,
		"resource_version":      gorm.Expr("resource_version + 1"),
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

func getNextRenderedVersion(annotations map[string]string) (string, error) {
	var currentRenderedVersion int64 = 0
	var err error
	renderedVersionString, ok := annotations[api.DeviceAnnotationRenderedVersion]
	if ok {
		currentRenderedVersion, err = strconv.ParseInt(renderedVersionString, 10, 64)
		if err != nil {
			return "", err
		}
	}

	currentRenderedVersion++
	return strconv.FormatInt(currentRenderedVersion, 10), nil
}

func (s *DeviceStore) UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications string) error {
	return retryUpdate(func() (bool, error) {
		return s.updateRendered(orgId, name, renderedConfig, renderedApplications)
	})
}

func (s *DeviceStore) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.RenderedDeviceSpec, error) {
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&device)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	annotations := util.LabelArrayToMap(device.Annotations)
	renderedVersion, ok := annotations[api.DeviceAnnotationRenderedVersion]
	if !ok {
		return nil, flterrors.ErrNoRenderedVersion
	}

	var console *api.DeviceConsole

	if val, ok := annotations[api.DeviceAnnotationConsole]; ok {
		console = &api.DeviceConsole{
			GRPCEndpoint: consoleGrpcEndpoint,
			SessionID:    val,
		}
	}

	// if we have a console request we ignore the rendered version
	// TODO: bump the rendered version instead?
	if console == nil && knownRenderedVersion != nil && renderedVersion == *knownRenderedVersion {
		return nil, nil
	}

	renderedConfig := api.RenderedDeviceSpec{
		RenderedVersion: renderedVersion,
		Config:          device.RenderedConfig,
		Os:              device.Spec.Data.Os,
		Systemd:         device.Spec.Data.Systemd,
		Resources:       device.Spec.Data.Resources,
		Console:         console,
		Applications:    device.RenderedApplications.Data,
	}

	return &renderedConfig, nil
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
	var repos model.RepositoryList
	err := s.db.Model(&device).Association("Repositories").Find(&repos)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	repositories, err := repos.ToApiResource(nil, nil)
	if err != nil {
		return nil, err
	}
	return &repositories, nil
}
