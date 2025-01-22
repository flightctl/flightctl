package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Device interface {
	InitialMigration() error

	Create(ctx context.Context, orgId uuid.UUID, device *api.Device, callback DeviceStoreCallback) (*api.Device, error)
	Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, callback DeviceStoreCallback) (*api.Device, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, callback DeviceStoreCallback) (*api.Device, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error)

	Summary(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DevicesSummary, error)
	UpdateSummaryStatusBatch(ctx context.Context, orgId uuid.UUID, deviceNames []string, status api.DeviceSummaryStatusType, statusInfo string) error
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications string) error
	GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.RenderedDeviceSpec, error)
	SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error)

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

	return nil
}

func (s *DeviceStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Device, callback DeviceStoreCallback) (*api.Device, error) {
	return s.genericStore.Create(ctx, orgId, resource, callback)
}

func (s *DeviceStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, callback DeviceStoreCallback) (*api.Device, error) {
	return s.genericStore.Update(ctx, orgId, resource, fieldsToUnset, fromAPI, validationCallback, callback)
}

func (s *DeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback DeviceStoreValidationCallback, callback DeviceStoreCallback) (*api.Device, bool, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, validationCallback, callback)
}

func (s *DeviceStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *DeviceStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
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
		nextRenderedVersion, err := getNextRenderedVersion(existingAnnotations)
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

	nextRenderedVersion, err := getNextRenderedVersion(existingAnnotations)
	if err != nil {
		return false, err
	}

	existingAnnotations[api.DeviceAnnotationRenderedVersion] = nextRenderedVersion

	renderedApplicationsJSON := renderedApplications
	if strings.TrimSpace(renderedApplications) == "" {
		renderedApplicationsJSON = "[]"
	}

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":           model.MakeJSONMap(existingAnnotations),
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

	annotations := util.EnsureMap(device.Annotations)
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
		UpdatePolicy:    device.Spec.Data.UpdatePolicy,
		Decommission:    device.Spec.Data.Decommissioning,
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
