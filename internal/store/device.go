package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Device interface {
	Create(ctx context.Context, orgId uuid.UUID, device *api.Device, callback DeviceStoreCallback) (*api.Device, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error)
	ListExpiredHeartbeats(ctx context.Context, expirationTime time.Time) (*model.DeviceList, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, callback DeviceStoreCallback) (*api.Device, bool, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	UpdateRendered(ctx context.Context, orgId uuid.UUID, name string, rendered string) error
	GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string) (*api.RenderedDeviceSpec, error)
	SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error
	OverrideTimeGetterForTesting(timeGetter TimeGetter)
	InitialMigration() error
}

type TimeGetter func() time.Time
type DeviceStore struct {
	db             *gorm.DB
	log            logrus.FieldLogger
	getCurrentTime TimeGetter
}

type DeviceStoreCallback func(before *model.Device, after *model.Device)
type DeviceStoreAllDeletedCallback func(orgId uuid.UUID)

// Make sure we conform to Device interface
var _ Device = (*DeviceStore)(nil)

func NewDevice(db *gorm.DB, log logrus.FieldLogger) Device {
	return &DeviceStore{db: db, log: log, getCurrentTime: time.Now}
}

func (s *DeviceStore) OverrideTimeGetterForTesting(timeGetter TimeGetter) {
	s.getCurrentTime = timeGetter
}

func (s *DeviceStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.Device{}); err != nil {
		return err
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
			return s.db.Migrator().CreateIndex(&model.Device{}, "Labels")
		}
	}

	return nil
}

func (s *DeviceStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Device, callback DeviceStoreCallback) (*api.Device, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	device := model.NewDeviceFromApiResource(resource)
	device.OrgID = orgId
	device.Generation = util.Int64ToPtr(1)

	result := s.db.Create(device)
	callback(nil, device)
	return resource, flterrors.ErrorFromGormError(result.Error)
}

func (s *DeviceStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error) {
	var devices model.DeviceList
	var nextContinue *string
	var numRemaining *int64

	query := BuildBaseListQuery(s.db.Model(&devices), orgId, listParams)
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
			countQuery := BuildBaseListQuery(s.db.Model(&devices), orgId, listParams)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiDevicelist := devices.ToApiResource(nextContinue, numRemaining)
	return &apiDevicelist, flterrors.ErrorFromGormError(result.Error)
}

func (s *DeviceStore) ListExpiredHeartbeats(ctx context.Context, expirationTime time.Time) (*model.DeviceList, error) {
	var devices model.DeviceList
	query := s.db.Where("heartbeat_timeout_at < ?", expirationTime)
	result := query.Find(&devices)
	return &devices, flterrors.ErrorFromGormError(result.Error)
}

func (s *DeviceStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error {
	condition := model.Device{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)

	if result.Error != nil {
		return flterrors.ErrorFromGormError(result.Error)
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
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	apiDevice := device.ToApiResource()
	return &apiDevice, nil
}

func (s *DeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Device, fieldsToUnset []string, fromAPI bool, callback DeviceStoreCallback) (*api.Device, bool, error) {
	if resource == nil {
		return nil, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, flterrors.ErrResourceNameIsNil
	}
	device := model.NewDeviceFromApiResource(resource)
	device.OrgID = orgId

	// Use the dedicated API to update annotations
	device.Annotations = nil

	created := false
	var existingRecord *model.Device

	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = &model.Device{Resource: model.Resource{OrgID: device.OrgID, Name: device.Name}}
		result := innerTx.First(existingRecord)

		deviceExists := true

		// NotFound is OK because in that case we will create the record, anything else is a real error
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				deviceExists = false
			} else {
				return flterrors.ErrorFromGormError(result.Error)
			}
		}
		// We returned
		if !deviceExists {
			created = true
			device.Generation = util.Int64ToPtr(1)

			result = innerTx.Create(device)
			if result.Error != nil {
				return flterrors.ErrorFromGormError(result.Error)
			}
		} else {
			sameSpec := reflect.DeepEqual(existingRecord.Spec.Data, device.Spec.Data)

			// Update the generation if the spec was updated
			if !sameSpec {
				if fromAPI && existingRecord.Owner != nil && len(*existingRecord.Owner) != 0 {
					return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
				}

				if existingRecord.Generation == nil {
					device.Generation = util.Int64ToPtr(1)
				} else {
					device.Generation = util.Int64ToPtr(*existingRecord.Generation + 1)
				}
			} else {
				device.Generation = existingRecord.Generation
			}

			where := model.Device{Resource: model.Resource{OrgID: device.OrgID, Name: device.Name}}
			query := innerTx.Model(where)

			selectFields := []string{"spec"}
			selectFields = append(selectFields, GetNonNilFieldsFromResource(device.Resource)...)
			if len(fieldsToUnset) > 0 {
				selectFields = append(selectFields, fieldsToUnset...)
			}
			query = query.Select(selectFields)
			result := query.Updates(&device)
			if result.Error != nil {
				return flterrors.ErrorFromGormError(result.Error)
			}
		}
		return nil
	})

	if err != nil {
		return nil, false, err
	}

	callback(existingRecord, device)

	updatedResource := device.ToApiResource()
	return &updatedResource, created, nil
}

// Method called only by agent, handles check-in timestamps as a side effect.
// Contains logic that doesn't belong in the store layer, but is here for efficiency.
func (s *DeviceStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Device) (*api.Device, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	var existingRecord *model.Device

	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = &model.Device{Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name}}
		result := innerTx.First(existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}

		// Update the time that the agent last reported in
		now := time.Now()
		resource.Status.LastSeenAt = util.StrToPtr(now.Format(time.RFC3339))

		updatedStatus := model.MakeJSONField(*resource.Status)
		updates := map[string]interface{}{
			"status": updatedStatus,
		}
		existingRecord.Status = updatedStatus

		// If a heartbeat threshold is defined, set the time in the DB to the expiration of the sooner one
		if existingRecord.Spec.Data.Settings != nil {
			var minTime *time.Duration
			if existingRecord.Spec.Data.Settings.HeartbeatWarningTime != nil {
				warningTime, err := time.ParseDuration(*existingRecord.Spec.Data.Settings.HeartbeatWarningTime)
				if err != nil {
					return fmt.Errorf("failed to parse HeartbeatWarningTime: %w", err)
				}
				minTime = &warningTime
			}
			if existingRecord.Spec.Data.Settings.HeartbeatErrorTime != nil {
				errorTime, err := time.ParseDuration(*existingRecord.Spec.Data.Settings.HeartbeatErrorTime)
				if err != nil {
					return fmt.Errorf("failed to parse HeartbeatErrorTime: %w", err)
				}
				if minTime != nil && errorTime < *minTime {
					minTime = &errorTime
				}
			}
			if minTime != nil {
				heartbeatTimeoutAt := time.Time.Add(now, *minTime)
				updates["heartbeat_timeout_at"] = heartbeatTimeoutAt
				existingRecord.HeartbeatTimeoutAt = heartbeatTimeoutAt
			}
		}

		// Unset the heartbeat warning and error conditions (has no effect if they are already unset)
		if existingRecord.ServiceConditions != nil && existingRecord.ServiceConditions.Data.Conditions != nil {
			okCondition := api.Condition{
				Type:   api.DeviceHeartbeatWarning,
				Status: api.ConditionStatusFalse,
			}
			api.SetStatusCondition(existingRecord.ServiceConditions.Data.Conditions, okCondition)
			okCondition.Type = api.DeviceHeartbeatError
			api.SetStatusCondition(existingRecord.ServiceConditions.Data.Conditions, okCondition)
			updates["service_conditions"] = existingRecord.ServiceConditions
		}

		result = innerTx.Model(existingRecord).Updates(updates)
		return flterrors.ErrorFromGormError(result.Error)
	})

	updatedResource := existingRecord.ToApiResource()
	return &updatedResource, flterrors.ErrorFromGormError(err)
}

func (s *DeviceStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error {
	var existingRecord model.Device
	log := log.WithReqIDFromCtx(ctx, s.log)
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}

		associatedRecord := model.EnrollmentRequest{Resource: model.Resource{OrgID: orgId, Name: name}}

		if err := innerTx.Unscoped().Delete(&existingRecord).Error; err != nil {
			return flterrors.ErrorFromGormError(err)
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

func (s *DeviceStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	return s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}
		existingAnnotations := util.LabelArrayToMap(existingRecord.Annotations)
		existingAnnotations = util.MergeLabels(existingAnnotations, annotations)

		for _, deleteKey := range deleteKeys {
			delete(existingAnnotations, deleteKey)
		}
		annotationsArray := util.LabelMapToArray(&existingAnnotations)

		result = innerTx.Model(existingRecord).Updates(map[string]interface{}{
			"annotations": pq.StringArray(annotationsArray),
		})
		return flterrors.ErrorFromGormError(result.Error)
	})
}

func (s *DeviceStore) UpdateRendered(ctx context.Context, orgId uuid.UUID, name string, rendered string) error {
	return s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}
		existingAnnotations := util.LabelArrayToMap(existingRecord.Annotations)

		var currentRenderedVersion int64 = 0
		renderedVersionString, ok := existingAnnotations[model.DeviceAnnotationRenderedVersion]
		if ok {
			currentRenderedVersion, err = strconv.ParseInt(renderedVersionString, 10, 64)
			if err != nil {
				return err
			}
		}

		currentRenderedVersion++
		existingAnnotations[model.DeviceAnnotationRenderedVersion] = strconv.FormatInt(currentRenderedVersion, 10)
		annotationsArray := util.LabelMapToArray(&existingAnnotations)

		result = innerTx.Model(existingRecord).Updates(map[string]interface{}{
			"annotations":     pq.StringArray(annotationsArray),
			"rendered_config": &rendered,
		})

		return flterrors.ErrorFromGormError(result.Error)
	})
}

func (s *DeviceStore) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string) (*api.RenderedDeviceSpec, error) {
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&device)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}

	annotations := util.LabelArrayToMap(device.Annotations)
	renderedVersion, ok := annotations[model.DeviceAnnotationRenderedVersion]
	if !ok {
		return nil, flterrors.ErrNoRenderedVersion
	}

	if knownRenderedVersion != nil && renderedVersion == *knownRenderedVersion {
		return nil, nil
	}

	renderedConfig := api.RenderedDeviceSpec{
		RenderedVersion: renderedVersion,
		Config:          device.RenderedConfig,
		Containers:      device.Spec.Data.Containers,
		Os:              device.Spec.Data.Os,
		Systemd:         device.Spec.Data.Systemd,
	}

	return &renderedConfig, nil
}

func (s *DeviceStore) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error {
	return s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return flterrors.ErrorFromGormError(result.Error)
		}

		if existingRecord.ServiceConditions == nil {
			existingRecord.ServiceConditions = model.MakeJSONField(model.ServiceConditions{})
		}
		if existingRecord.ServiceConditions.Data.Conditions == nil {
			existingRecord.ServiceConditions.Data.Conditions = &[]api.Condition{}
		}

		for _, condition := range conditions {
			_ = api.SetStatusCondition(existingRecord.ServiceConditions.Data.Conditions, condition)
		}

		result = innerTx.Model(existingRecord).Updates(map[string]interface{}{
			"service_conditions": existingRecord.ServiceConditions,
		})
		return flterrors.ErrorFromGormError(result.Error)
	})
}
