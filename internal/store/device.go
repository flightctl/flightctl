package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type DeviceStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

// Make sure we conform to DeviceStore interface
var _ service.DeviceStore = (*DeviceStore)(nil)

func NewDeviceStore(db *gorm.DB, log logrus.FieldLogger) *DeviceStore {
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

func (s *DeviceStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Device) (*api.Device, error) {
	log := log.WithReqIDFromCtx(ctx, s.log)
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	device := model.NewDeviceFromApiResource(resource)
	device.OrgID = orgId

	result := s.db.Create(device)
	log.Printf("db.Create(%s): %d rows affected, error is %v", device, result.RowsAffected, result.Error)
	return resource, result.Error
}

func (s *DeviceStore) List(ctx context.Context, orgId uuid.UUID, listParams service.ListParams) (*api.DeviceList, error) {
	var devices model.DeviceList
	var nextContinue *string
	var numRemaining *int64
	log := log.WithReqIDFromCtx(ctx, s.log)

	query := BuildBaseListQuery(s.db.Model(&devices), orgId, listParams.Labels)
	// Request 1 more than the user asked for to see if we need to return "continue"
	query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	result := query.Find(&devices)
	log.Printf("db.Find(): %d rows affected, error is %v", result.RowsAffected, result.Error)

	// If we got more than the user requested, remove one record and calculate "continue"
	if len(devices) > listParams.Limit {
		nextContinueStruct := service.Continue{
			Name:    devices[len(devices)-1].Name,
			Version: service.CurrentContinueVersion,
		}
		devices = devices[:len(devices)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery := BuildBaseListQuery(s.db.Model(&devices), orgId, listParams.Labels)
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

// Get all Devices regardless of ownership. Used internally by the DeviceUpdater.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *DeviceStore) ListIgnoreOrg(labels map[string]string) ([]model.Device, error) {
	var devices model.DeviceList

	query := LabelSelectionQuery(s.db, labels).Order("name")
	result := query.Model(&devices).Find(&devices)
	s.log.Debugf("db.Find(): %d rows affected, error is %v", result.RowsAffected, result.Error)
	if result.Error != nil {
		return nil, result.Error
	}
	return devices, nil
}

// Update a Device regardless of ownership. Used internally by the DeviceUpdater.
func (s *DeviceStore) UpdateIgnoreOrg(device *model.Device) error {
	var updatedDevice model.Device
	where := model.Device{Resource: model.Resource{Name: device.Name, OrgID: device.OrgID}}
	return s.db.Where(where).Assign(device).FirstOrCreate(&updatedDevice).Error
}

func (s *DeviceStore) DeleteAll(ctx context.Context, orgId uuid.UUID) error {
	log := log.WithReqIDFromCtx(ctx, s.log)
	condition := model.Device{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	log.Printf("db.Delete(): %d rows affected, error is %v", result.RowsAffected, result.Error)
	return result.Error
}

func (s *DeviceStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	log := log.WithReqIDFromCtx(ctx, s.log)
	device := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.First(&device)
	log.Printf("db.Find(%s): %d rows affected, error is %v", device, result.RowsAffected, result.Error)
	if result.Error != nil {
		return nil, result.Error
	}
	apiDevice := device.ToApiResource()
	return &apiDevice, nil
}

func (s *DeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Device) (*api.Device, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("resource is nil")
	}
	device := model.NewDeviceFromApiResource(resource)
	device.OrgID = orgId

	// don't overwrite status
	device.Status = nil

	created := false
	findDevice := model.Device{Resource: model.Resource{Name: *resource.Metadata.Name}}
	findDevice.OrgID = orgId
	result := s.db.First(&findDevice)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			created = true
		} else {
			return nil, false, result.Error
		}
	}

	var updatedDevice model.Device
	where := model.Device{Resource: model.Resource{Name: device.Name}}
	where.OrgID = orgId
	result = s.db.Where(where).Assign(device).FirstOrCreate(&updatedDevice)

	updatedResource := updatedDevice.ToApiResource()
	return &updatedResource, created, result.Error
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

func (s *DeviceStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	condition := model.Device{
		Resource: model.Resource{OrgID: orgId, Name: name},
	}
	result := s.db.Unscoped().Delete(&condition)
	return result.Error
}
