package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fromAPI bool, callback DeviceStoreCallback) (*api.Device, bool, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error
	UpdateTemplateVersionAndOwner(ctx context.Context, orgId uuid.UUID, name string, tv string, owner *string, callback DeviceStoreCallback) error
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownOwner, knownTemplateVersion *string) (*api.RenderedDeviceSpec, error)
	InitialMigration() error
}

type DeviceStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

type DeviceStoreCallback func(before *model.Device, after *model.Device)
type DeviceStoreAllDeletedCallback func(orgId uuid.UUID)

// Make sure we conform to Device interface
var _ Device = (*DeviceStore)(nil)

func NewDevice(db *gorm.DB, log logrus.FieldLogger) Device {
	return &DeviceStore{db: db, log: log}
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
		return nil, fmt.Errorf("resource is nil")
	}
	device := model.NewDeviceFromApiResource(resource)
	device.OrgID = orgId
	device.Generation = util.Int64ToPtr(1)

	result := s.db.Create(device)
	callback(nil, device)
	return resource, result.Error
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
	return &apiDevicelist, result.Error
}

func (s *DeviceStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback DeviceStoreAllDeletedCallback) error {
	condition := model.Device{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)

	if result.Error != nil {
		return result.Error
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
		return nil, result.Error
	}
	apiDevice := device.ToApiResource()
	return &apiDevice, nil
}

func (s *DeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Device, fromAPI bool, callback DeviceStoreCallback) (*api.Device, bool, error) {
	log := log.WithReqIDFromCtx(ctx, s.log)
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	device := model.NewDeviceFromApiResource(resource)
	if device.Name == "" {
		return nil, false, fmt.Errorf("resource has no name")
	}
	device.OrgID = orgId

	// don't overwrite status, generation, or owner
	device.Status = nil
	device.Generation = nil
	device.Owner = nil

	created := false
	var existingRecord *model.Device

	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = &model.Device{Resource: model.Resource{OrgID: device.OrgID, Name: device.Name}}
		result := innerTx.First(existingRecord)
		log.Printf("db.Find(%s): %d rows affected, error is %v", *existingRecord, result.RowsAffected, result.Error)

		deviceExists := true

		// NotFound is OK because in that case we will create the record, anything else is a real error
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				deviceExists = false
			} else {
				return result.Error
			}
		}
		// We returned
		if !deviceExists {
			created = true
			device.Generation = util.Int64ToPtr(1)

			result = innerTx.Create(device)
			if result.Error != nil {
				return result.Error
			}
		} else {
			sameSpec := reflect.DeepEqual(existingRecord.Spec.Data, device.Spec.Data)
			if fromAPI && util.DefaultIfNil(existingRecord.Spec.Data.TemplateVersion, "") != util.DefaultIfNil(device.Spec.Data.TemplateVersion, "") {
				return gorm.ErrInvalidData
			}

			// Update the generation if the spec was updated
			if !sameSpec {
				if existingRecord.Owner != nil && len(*existingRecord.Owner) != 0 {
					return fmt.Errorf("cannot update a device owned by a fleet")
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
			result = innerTx.Model(where).Updates(&device)
			if result.Error != nil {
				return result.Error
			}
		}
		return nil
	})

	if err != nil {
		return nil, false, err
	}

	if existingRecord != nil {
		existingRecord.Owner = nil // Match the incoming device
	}
	callback(existingRecord, device)

	updatedResource := device.ToApiResource()
	return &updatedResource, created, nil
}

func (s *DeviceStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Device) (*api.Device, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	if resource.Metadata.Name == nil {
		return nil, fmt.Errorf("resource.metadata.name is nil")
	}
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := s.db.Model(&device).Updates(map[string]interface{}{
		"status": model.MakeJSONField(resource.Status),
	})
	return resource, result.Error
}

func (s *DeviceStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback DeviceStoreCallback) error {
	var existingRecord model.Device
	log := log.WithReqIDFromCtx(ctx, s.log)
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return result.Error
		}

		associatedRecord := model.EnrollmentRequest{Resource: model.Resource{OrgID: orgId, Name: name}}

		if err := innerTx.Unscoped().Delete(&existingRecord).Error; err != nil {
			return err
		}

		if err := innerTx.Unscoped().Delete(&associatedRecord).Error; err != nil {
			log.Warningf("failed to delete associated enrollment request: %v", err)
		}

		return nil
	})

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	callback(&existingRecord, nil)
	return nil
}

// Sets spec.templateVersion and owner
func (s *DeviceStore) UpdateTemplateVersionAndOwner(ctx context.Context, orgId uuid.UUID, name string, tv string, owner *string, callback DeviceStoreCallback) error {
	var existingRecord *model.Device
	var updatedRecord *model.Device
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord = &model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(existingRecord)
		if result.Error != nil {
			return result.Error
		}

		updatedDevice := model.Device{
			Resource: model.Resource{
				Name:       existingRecord.Name,
				Generation: util.Int64ToPtr(*existingRecord.Generation + 1),
			},
			Spec: model.MakeJSONField(api.DeviceSpec{TemplateVersion: &tv}),
		}
		if owner != nil {
			updatedDevice.Owner = owner
		}
		updatedRecord = &updatedDevice

		where := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result = innerTx.Model(where).Updates(&updatedDevice)
		return result.Error
	})
	if err != nil {
		return err
	}

	callback(existingRecord, updatedRecord)
	return nil
}

func (s *DeviceStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	return s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		existingRecord := model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(&existingRecord)
		if result.Error != nil {
			return result.Error
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
		return result.Error
	})
}

func (s *DeviceStore) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownOwner, knownTemplateVersion *string) (*api.RenderedDeviceSpec, error) {
	var deviceOwner string
	var templateVersion *model.TemplateVersion
	err := s.db.Transaction(func(innerTx *gorm.DB) (err error) {
		device := &model.Device{Resource: model.Resource{OrgID: orgId, Name: name}}
		result := innerTx.First(device)
		if result.Error != nil {
			return result.Error
		}

		if device.Owner == nil || device.Spec.Data.TemplateVersion == nil {
			return gorm.ErrInvalidData
		}
		deviceOwner = *device.Owner

		if knownOwner != nil && knownTemplateVersion != nil && *device.Owner == *knownOwner && *device.Spec.Data.TemplateVersion == *knownTemplateVersion {
			return nil
		}

		templateVersion = &model.TemplateVersion{
			ResourceWithPrimaryKeyOwner: model.ResourceWithPrimaryKeyOwner{OrgID: orgId, Owner: device.Owner, Name: *device.Spec.Data.TemplateVersion},
		}
		result = s.db.First(templateVersion)
		if result.Error != nil {
			return result.Error
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	if templateVersion == nil {
		return nil, nil
	}

	if templateVersion.Valid == nil || !*templateVersion.Valid {
		return nil, gorm.ErrInvalidData
	}

	renderedConfig := api.RenderedDeviceSpec{
		Owner:           deviceOwner,
		TemplateVersion: templateVersion.Name,
		Config:          templateVersion.RenderedConfig,
		Containers:      templateVersion.Status.Data.Containers,
		Os:              templateVersion.Status.Data.Os,
		Systemd:         templateVersion.Status.Data.Systemd,
	}

	return &renderedConfig, nil
}
