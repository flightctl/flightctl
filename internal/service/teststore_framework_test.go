package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const statusSuccessCode = int32(http.StatusOK)
const statusCreatedCode = int32(http.StatusCreated)
const statusFailedCode = int32(http.StatusInternalServerError)
const statusBadRequestCode = int32(http.StatusBadRequest)
const statusNotFoundCode = int32(http.StatusNotFound)

type TestStore struct {
	store.Store
	devices            *DummyDevice
	events             *DummyEvent
	fleets             *DummyFleet
	repositories       *DummyRepository
	resourceSyncVals   *DummyResourceSync
	enrollmentRequests *DummyEnrollmentRequest
}

type DummyDevice struct {
	store.Device
	devices *[]api.Device
}

type DummyEvent struct {
	store.Event
	events *[]api.Event
}

type DummyFleet struct {
	store.Fleet
	fleets *[]api.Fleet
}

type DummyRepository struct {
	store.Repository
	repositories *[]api.Repository
}

type DummyResourceSync struct {
	store.ResourceSync
	resourceSyncVals *[]api.ResourceSync
}

type DummyEnrollmentRequest struct {
	store.EnrollmentRequestStore
	enrollmentRequests *[]api.EnrollmentRequest
}

func (s *TestStore) init() {
	if s.events == nil {
		s.events = &DummyEvent{events: &[]api.Event{}}
	}
	if s.devices == nil {
		s.devices = &DummyDevice{devices: &[]api.Device{}}
	}
	if s.fleets == nil {
		s.fleets = &DummyFleet{fleets: &[]api.Fleet{}}
	}
	if s.repositories == nil {
		s.repositories = &DummyRepository{repositories: &[]api.Repository{}}
	}
	if s.resourceSyncVals == nil {
		s.resourceSyncVals = &DummyResourceSync{resourceSyncVals: &[]api.ResourceSync{}}
	}
	if s.enrollmentRequests == nil {
		s.enrollmentRequests = &DummyEnrollmentRequest{enrollmentRequests: &[]api.EnrollmentRequest{}}
	}
}

func (s *TestStore) Fleet() store.Fleet {
	s.init()
	return s.fleets
}

func (s *TestStore) Device() store.Device {
	s.init()
	return s.devices
}

func (s *TestStore) Event() store.Event {
	s.init()
	return s.events
}

func (s *TestStore) ResourceSync() store.ResourceSync {
	s.init()
	return s.resourceSyncVals
}

func (s *TestStore) Repository() store.Repository {
	s.init()
	return s.repositories
}

func (s *TestStore) EnrollmentRequest() store.EnrollmentRequest {
	s.init()
	return s.enrollmentRequests
}

// --------------------------------------> Event

func (s *DummyEvent) Create(ctx context.Context, orgId uuid.UUID, event *api.Event) error {
	var ev api.Event
	deepCopy(event, &ev)
	*s.events = append(*s.events, ev)
	return nil
}

func (s *DummyEvent) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*api.EventList, error) {
	list := &api.EventList{
		ApiVersion: "",
		Kind:       "",
		Metadata:   api.ListMeta{},
		Items:      *s.events,
	}
	return list, nil
}

// --------------------------------------> Device

func (s *DummyDevice) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	for _, device := range *s.devices {
		if name == *device.Metadata.Name {
			var dev api.Device
			deepCopy(device, &dev)
			return &dev, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyDevice) Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback, callbackEvent store.EventCallback) (*api.Device, error) {
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			var oldDevice api.Device
			deepCopy(dev, &oldDevice)
			var d api.Device
			deepCopy(device, &d)
			if validationCallback != nil {
				// TODO
				if err := validationCallback(ctx, &oldDevice, &d); err != nil {
					return nil, err
				}
			}
			if callback != nil {
				callback(ctx, store.NullOrgId, &oldDevice, &d)
			}
			(*s.devices)[i] = d
			if callbackEvent != nil {
				callbackEvent(ctx, api.DeviceKind, orgId, lo.FromPtr(device.Metadata.Name), &oldDevice, &d, false, nil, nil)
			}
			return device, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound

}

func (s *DummyDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback, callbackEvent store.EventCallback) (*api.Device, bool, api.ResourceUpdatedDetails, error) {
	created := true
	var d api.Device
	deepCopy(device, &d)
	var oldDevice api.Device
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			deepCopy(dev, &oldDevice)
			*s.devices = append((*s.devices)[:i], (*s.devices)[i+1:]...)
			created = false
			break
		}
	}
	details := api.ResourceUpdatedDetails{}
	// TODO: update found device
	if !created {
		if validationCallback != nil {
			if err := validationCallback(ctx, &oldDevice, &d); err != nil {
				return nil, created, api.ResourceUpdatedDetails{}, err
			}
		}
		if callback != nil {
			callback(ctx, store.NullOrgId, &oldDevice, device)
		}
	}
	*s.devices = append(*s.devices, d)
	if callbackEvent != nil {
		callbackEvent(ctx, api.DeviceKind, orgId, lo.FromPtr(device.Metadata.Name), &oldDevice, &d, created, nil, nil)
	}
	return &d, created, details, nil
}

func (s *DummyDevice) Create(ctx context.Context, orgId uuid.UUID, device *api.Device, callback store.DeviceStoreCallback, callbackEvent store.EventCallback) (*api.Device, error) {
	var d api.Device
	deepCopy(device, &d)
	*s.devices = append(*s.devices, d)
	if callbackEvent != nil {
		callbackEvent(ctx, api.DeviceKind, orgId, lo.FromPtr(d.Metadata.Name), nil, device, true, nil, nil)
	}
	return device, nil
}

func (s *DummyDevice) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device, callbackEvent store.EventCallback) (*api.Device, error) {
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			var oldDevice api.Device
			deepCopy(dev, &oldDevice)
			var d api.Device
			deepCopy(device, &d)
			status := dev.Status
			if status != nil {
				oldDevice.Status = lo.ToPtr(lo.FromPtr(status))
			}
			(*s.devices)[i].Status = d.Status
			if callbackEvent != nil {
				callbackEvent(ctx, api.DeviceKind, orgId, lo.FromPtr(d.Metadata.Name), &oldDevice, &d, false, nil, nil)
			}
			return &d, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> Fleet

func (s *DummyFleet) Get(ctx context.Context, orgId uuid.UUID, name string, options ...store.GetOption) (*api.Fleet, error) {
	for _, fleet := range *s.fleets {
		if name == *fleet.Metadata.Name {
			var f api.Fleet
			deepCopy(fleet, &f)
			return &f, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyFleet) Create(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback store.FleetStoreCallback, callbackEvent store.EventCallback) (*api.Fleet, error) {
	var f api.Fleet
	deepCopy(fleet, &f)
	*s.fleets = append(*s.fleets, f)
	if callbackEvent != nil {
		callbackEvent(ctx, api.FleetKind, orgId, lo.FromPtr(fleet.Metadata.Name), nil, fleet, true, nil, nil)
	}
	return fleet, nil
}

func (s *DummyFleet) Update(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.FleetStoreCallback, callbackEvent store.EventCallback) (*api.Fleet, error) {
	for i, flt := range *s.fleets {
		if *fleet.Metadata.Name == *flt.Metadata.Name {
			var f api.Fleet
			if callbackEvent != nil {
				callbackEvent(ctx, api.FleetKind, orgId, lo.FromPtr(fleet.Metadata.Name), &(*s.fleets)[i], fleet, false, nil, nil)
			}
			deepCopy(fleet, &f)
			(*s.fleets)[i] = f
			return &f, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> Repository

func (s *DummyRepository) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error) {
	for _, repo := range *s.repositories {
		if name == *repo.Metadata.Name {
			var r api.Repository
			deepCopy(repo, &r)
			return &r, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) Create(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback, callbackEvent store.EventCallback) (*api.Repository, error) {
	var r api.Repository
	deepCopy(repository, &r)
	*s.repositories = append(*s.repositories, r)
	if callbackEvent != nil {
		callbackEvent(ctx, api.RepositoryKind, orgId, lo.FromPtr(repository.Metadata.Name), nil, repository, true, nil, nil)
	}
	return repository, nil
}

func (s *DummyRepository) Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback, callbackEvent store.EventCallback) (*api.Repository, error) {
	for i, repo := range *s.repositories {
		if *repository.Metadata.Name == *repo.Metadata.Name {
			if callbackEvent != nil {
				callbackEvent(ctx, api.RepositoryKind, orgId, lo.FromPtr(repository.Metadata.Name), (*s.repositories)[i], repository, false, nil, nil)
			}
			var r api.Repository
			deepCopy(repository, &r)
			(*s.repositories)[i] = r
			return repository, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback, callbackEvent store.EventCallback) (*api.Repository, bool, error) {
	return nil, false, fmt.Errorf("CreateOrUpdate not implemented for DummyRepository")
}

func (s *DummyRepository) UpdateStatus(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callbackEvent store.EventCallback) (*api.Repository, error) {
	for i, repo := range *s.repositories {
		if *repository.Metadata.Name == *repo.Metadata.Name {
			var oldRepo api.Repository
			deepCopy(repo, &oldRepo)
			var r api.Repository
			deepCopy(repository, &r)
			status := repo.Status
			if status != nil {
				oldRepo.Status = lo.ToPtr(lo.FromPtr(status))
			}
			(*s.repositories)[i].Status = r.Status
			if callbackEvent != nil {
				callbackEvent(ctx, api.DeviceKind, orgId, lo.FromPtr(r.Metadata.Name), &oldRepo, &r, false, nil, nil)
			}
			return &r, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound

}

func (s *DummyRepository) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*api.RepositoryList, error) {
	return &api.RepositoryList{
		ApiVersion: "",
		Kind:       "",
		Metadata:   api.ListMeta{},
		Items:      *s.repositories,
	}, nil
}

// --------------------------------------> ResourceSync

func (s *DummyResourceSync) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	for _, res := range *s.resourceSyncVals {
		if name == *res.Metadata.Name {
			var r api.ResourceSync
			deepCopy(res, &r)
			return &r, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) Create(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent store.EventCallback) (*api.ResourceSync, error) {
	var r api.ResourceSync
	deepCopy(resourceSync, &r)
	*s.resourceSyncVals = append(*s.resourceSyncVals, r)
	if callbackEvent != nil {
		callbackEvent(ctx, api.ResourceSyncKind, orgId, lo.FromPtr(resourceSync.Metadata.Name), nil, resourceSync, true, nil, nil)
	}
	return resourceSync, nil
}

func (s *DummyResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent store.EventCallback) (*api.ResourceSync, error) {
	for i, sync := range *s.resourceSyncVals {
		if *resourceSync.Metadata.Name == *sync.Metadata.Name {
			if callbackEvent != nil {
				callbackEvent(ctx, api.ResourceSyncKind, orgId, lo.FromPtr(resourceSync.Metadata.Name), (*s.resourceSyncVals)[i], resourceSync, false, nil, nil)
			}
			var r api.ResourceSync
			deepCopy(resourceSync, &r)
			(*s.resourceSyncVals)[i] = r
			return resourceSync, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent store.EventCallback) (*api.ResourceSync, bool, error) {
	var oldRs *api.ResourceSync
	created := true
	for i, resource := range *s.resourceSyncVals {
		if *resourceSync.Metadata.Name == *resource.Metadata.Name {
			oldRs = &(*s.resourceSyncVals)[i]
			*s.resourceSyncVals = append((*s.resourceSyncVals)[:i], (*s.resourceSyncVals)[i+1:]...)
			created = false
			break
		}
	}
	// TODO: update found device
	// resourceSync -> oldResourceSync
	var r api.ResourceSync
	deepCopy(resourceSync, &r)
	*s.resourceSyncVals = append(*s.resourceSyncVals, r)
	if callbackEvent != nil {
		callbackEvent(ctx, api.ResourceSyncKind, orgId, lo.FromPtr(resourceSync.Metadata.Name), oldRs, resourceSync, created, nil, nil)
	}
	return resourceSync, created, nil
}

func (s *DummyResourceSync) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*api.ResourceSyncList, error) {
	return &api.ResourceSyncList{
		ApiVersion: "",
		Kind:       "",
		Metadata:   api.ListMeta{},
		Items:      *s.resourceSyncVals,
	}, nil
}

func (s *DummyResourceSync) UpdateStatus(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, error) {
	for i, rs := range *s.resourceSyncVals {
		if *resourceSync.Metadata.Name == *rs.Metadata.Name {
			var r api.ResourceSync
			deepCopy(resourceSync, &r)
			(*s.resourceSyncVals)[i].Status = r.Status
			return resourceSync, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> EnrollmentRequest

func (s *DummyEnrollmentRequest) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error) {
	for _, enrollment := range *s.enrollmentRequests {
		if name == *enrollment.Metadata.Name {
			var e api.EnrollmentRequest
			deepCopy(enrollment, &e)
			return &e, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyEnrollmentRequest) Create(ctx context.Context, orgId uuid.UUID, er *api.EnrollmentRequest, callbackEvent store.EventCallback) (*api.EnrollmentRequest, error) {
	var e api.EnrollmentRequest
	deepCopy(er, &e)
	*s.enrollmentRequests = append(*s.enrollmentRequests, e)
	if callbackEvent != nil {
		callbackEvent(ctx, api.EnrollmentRequestKind, orgId, lo.FromPtr(er.Metadata.Name), nil, er, true, nil, nil)
	}
	return er, nil
}

func (s *DummyEnrollmentRequest) UpdateStatus(ctx context.Context, orgId uuid.UUID, er *api.EnrollmentRequest, callbackEvent store.EventCallback) (*api.EnrollmentRequest, error) {
	for i, e := range *s.enrollmentRequests {
		if *er.Metadata.Name == *e.Metadata.Name {
			oldEr := (*s.enrollmentRequests)[i]
			if callbackEvent != nil {
				callbackEvent(ctx, api.EnrollmentRequestKind, orgId, lo.FromPtr(er.Metadata.Name), oldEr, er, false, nil, nil)
			}
			oldEr.Status = er.Status
			return er, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> CallbackManager

type dummyPublisher struct{}

func (d *dummyPublisher) Publish(_ context.Context, _ []byte) error {
	return nil
}

func (d *dummyPublisher) Close() {}

func dummyCallbackManager() tasks_client.CallbackManager {
	return tasks_client.NewCallbackManager(&dummyPublisher{}, logrus.New())
}

func deepCopy(src, dst interface{}) {
	data, err := json.Marshal(src)
	if err != nil {
		panic(fmt.Sprintf("deepCopy failed in test: %v", err))
	}
	if err = json.Unmarshal(data, dst); err != nil {
		panic(fmt.Sprintf("deepCopy failed in test: %v", err))
	}
}
