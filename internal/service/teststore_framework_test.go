package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
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
	organizations      *DummyOrganization
}

type DummyDevice struct {
	store.Device
	devices *[]domain.Device
}

type DummyEvent struct {
	store.Event
	events *[]domain.Event
}

type DummyFleet struct {
	store.Fleet
	fleets *[]domain.Fleet
}

type DummyRepository struct {
	store.Repository
	repositories *[]domain.Repository
}

type DummyResourceSync struct {
	store.ResourceSync
	resourceSyncVals *[]domain.ResourceSync
}

type DummyEnrollmentRequest struct {
	store.EnrollmentRequestStore
	enrollmentRequests *[]domain.EnrollmentRequest
}

type DummyOrganization struct {
	store.Organization
	organizations *[]*model.Organization
	err           error
}

func (s *TestStore) init() {
	if s.events == nil {
		s.events = &DummyEvent{events: &[]domain.Event{}}
	}
	if s.devices == nil {
		s.devices = &DummyDevice{devices: &[]domain.Device{}}
	}
	if s.fleets == nil {
		s.fleets = &DummyFleet{fleets: &[]domain.Fleet{}}
	}
	if s.repositories == nil {
		s.repositories = &DummyRepository{repositories: &[]domain.Repository{}}
	}
	if s.resourceSyncVals == nil {
		s.resourceSyncVals = &DummyResourceSync{resourceSyncVals: &[]domain.ResourceSync{}}
	}
	if s.enrollmentRequests == nil {
		s.enrollmentRequests = &DummyEnrollmentRequest{enrollmentRequests: &[]domain.EnrollmentRequest{}}
	}
	if s.organizations == nil {
		s.organizations = &DummyOrganization{organizations: &[]*model.Organization{}}
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

func (s *TestStore) Organization() store.Organization {
	s.init()
	return s.organizations
}

// --------------------------------------> Event

func (s *DummyEvent) Create(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
	var ev domain.Event
	deepCopy(event, &ev)
	*s.events = append(*s.events, ev)
	return nil
}

func (s *DummyEvent) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.EventList, error) {
	list := &domain.EventList{
		ApiVersion: "",
		Kind:       "",
		Metadata:   domain.ListMeta{},
		Items:      *s.events,
	}
	return list, nil
}

// --------------------------------------> Device

func (s *DummyDevice) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
	for _, device := range *s.devices {
		if name == *device.Metadata.Name {
			var dev domain.Device
			deepCopy(device, &dev)
			return &dev, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyDevice) GetWithoutServiceConditions(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
	return s.Get(ctx, orgId, name)
}

func (s *DummyDevice) Update(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callbackEvent store.EventCallback) (*domain.Device, error) {
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			var oldDevice domain.Device
			deepCopy(dev, &oldDevice)
			var d domain.Device
			deepCopy(device, &d)
			if validationCallback != nil {
				// TODO
				if err := validationCallback(ctx, &oldDevice, &d); err != nil {
					return nil, err
				}
			}
			(*s.devices)[i] = d
			if callbackEvent != nil {
				callbackEvent(ctx, domain.DeviceKind, orgId, lo.FromPtr(device.Metadata.Name), &oldDevice, &d, false, nil)
			}
			return device, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound

}

func (s *DummyDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callbackEvent store.EventCallback) (*domain.Device, bool, error) {
	created := true
	var d domain.Device
	deepCopy(device, &d)
	var oldDevice domain.Device
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			deepCopy(dev, &oldDevice)
			*s.devices = append((*s.devices)[:i], (*s.devices)[i+1:]...)
			created = false
			break
		}
	}
	// TODO: update found device
	if !created {
		if validationCallback != nil {
			if err := validationCallback(ctx, &oldDevice, &d); err != nil {
				return nil, created, err
			}
		}
	}
	*s.devices = append(*s.devices, d)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.DeviceKind, orgId, lo.FromPtr(device.Metadata.Name), &oldDevice, &d, created, nil)
	}
	return &d, created, nil
}

func (s *DummyDevice) Create(ctx context.Context, orgId uuid.UUID, device *domain.Device, callbackEvent store.EventCallback) (*domain.Device, error) {
	var d domain.Device
	deepCopy(device, &d)
	*s.devices = append(*s.devices, d)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.DeviceKind, orgId, lo.FromPtr(d.Metadata.Name), nil, device, true, nil)
	}
	return device, nil
}

func (s *DummyDevice) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *domain.Device, callbackEvent store.EventCallback) (*domain.Device, error) {
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			var oldDevice domain.Device
			deepCopy(dev, &oldDevice)
			var d domain.Device
			deepCopy(device, &d)
			// Update the device status
			(*s.devices)[i].Status = d.Status
			if callbackEvent != nil {
				callbackEvent(ctx, domain.DeviceKind, orgId, lo.FromPtr(d.Metadata.Name), &oldDevice, &d, false, nil)
			}
			return &d, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> Fleet

func (s *DummyFleet) Get(ctx context.Context, orgId uuid.UUID, name string, options ...store.GetOption) (*domain.Fleet, error) {
	for _, fleet := range *s.fleets {
		if name == *fleet.Metadata.Name {
			var f domain.Fleet
			deepCopy(fleet, &f)
			return &f, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyFleet) Create(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, callbackEvent store.EventCallback) (*domain.Fleet, error) {
	var f domain.Fleet
	deepCopy(fleet, &f)
	*s.fleets = append(*s.fleets, f)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.FleetKind, orgId, lo.FromPtr(fleet.Metadata.Name), nil, fleet, true, nil)
	}
	return fleet, nil
}

func (s *DummyFleet) Update(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, fieldsToUnset []string, fromAPI bool, callbackEvent store.EventCallback) (*domain.Fleet, error) {
	for i, flt := range *s.fleets {
		if *fleet.Metadata.Name == *flt.Metadata.Name {
			var f domain.Fleet
			if callbackEvent != nil {
				callbackEvent(ctx, domain.FleetKind, orgId, lo.FromPtr(fleet.Metadata.Name), &(*s.fleets)[i], fleet, false, nil)
			}
			deepCopy(fleet, &f)
			(*s.fleets)[i] = f
			return &f, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> Repository

func (s *DummyRepository) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, error) {
	for _, repo := range *s.repositories {
		if name == *repo.Metadata.Name {
			var r domain.Repository
			deepCopy(repo, &r)
			return &r, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) Create(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, callbackEvent store.EventCallback) (*domain.Repository, error) {
	var repo domain.Repository
	deepCopy(repository, &repo)
	*s.repositories = append(*s.repositories, repo)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.RepositoryKind, orgId, lo.FromPtr(repository.Metadata.Name), nil, &repo, true, nil)
	}
	return &repo, nil
}

func (s *DummyRepository) Update(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, callbackEvent store.EventCallback) (*domain.Repository, error) {
	for i, r := range *s.repositories {
		if *repository.Metadata.Name == *r.Metadata.Name {
			var oldRepo domain.Repository
			deepCopy(r, &oldRepo)
			var repo domain.Repository
			deepCopy(repository, &repo)
			(*s.repositories)[i] = repo
			if callbackEvent != nil {
				callbackEvent(ctx, domain.RepositoryKind, orgId, lo.FromPtr(repository.Metadata.Name), &oldRepo, &repo, false, nil)
			}
			return &repo, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, callbackEvent store.EventCallback) (*domain.Repository, bool, error) {
	created := true
	var repo domain.Repository
	deepCopy(repository, &repo)
	var oldRepo domain.Repository
	for i, r := range *s.repositories {
		if *repository.Metadata.Name == *r.Metadata.Name {
			deepCopy(r, &oldRepo)
			*s.repositories = append((*s.repositories)[:i], (*s.repositories)[i+1:]...)
			created = false
			break
		}
	}
	*s.repositories = append(*s.repositories, repo)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.RepositoryKind, orgId, lo.FromPtr(repository.Metadata.Name), &oldRepo, &repo, created, nil)
	}
	return &repo, created, nil
}

func (s *DummyRepository) UpdateStatus(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, callbackEvent store.EventCallback) (*domain.Repository, error) {
	for i, repo := range *s.repositories {
		if *repository.Metadata.Name == *repo.Metadata.Name {
			var oldRepo domain.Repository
			deepCopy(repo, &oldRepo)
			var r domain.Repository
			deepCopy(repository, &r)
			status := repo.Status
			if status != nil {
				oldRepo.Status = lo.ToPtr(lo.FromPtr(status))
			}
			(*s.repositories)[i].Status = r.Status
			if callbackEvent != nil {
				callbackEvent(ctx, domain.RepositoryKind, orgId, lo.FromPtr(r.Metadata.Name), &oldRepo, &r, false, nil)
			}
			return &r, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound

}

func (s *DummyRepository) Delete(ctx context.Context, orgId uuid.UUID, name string, callbackEvent store.EventCallback) error {
	for i, repo := range *s.repositories {
		if name == *repo.Metadata.Name {
			var oldRepo domain.Repository
			deepCopy(repo, &oldRepo)
			*s.repositories = append((*s.repositories)[:i], (*s.repositories)[i+1:]...)
			if callbackEvent != nil {
				callbackEvent(ctx, domain.RepositoryKind, orgId, name, &oldRepo, nil, false, nil)
			}
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
}

func (s *DummyRepository) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.RepositoryList, error) {
	return &domain.RepositoryList{
		ApiVersion: "",
		Kind:       "",
		Metadata:   domain.ListMeta{},
		Items:      *s.repositories,
	}, nil
}

// --------------------------------------> ResourceSync

func (s *DummyResourceSync) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, error) {
	for _, res := range *s.resourceSyncVals {
		if name == *res.Metadata.Name {
			var r domain.ResourceSync
			deepCopy(res, &r)
			return &r, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) Create(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, error) {
	var r domain.ResourceSync
	deepCopy(resourceSync, &r)
	*s.resourceSyncVals = append(*s.resourceSyncVals, r)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.ResourceSyncKind, orgId, lo.FromPtr(resourceSync.Metadata.Name), nil, resourceSync, true, nil)
	}
	return resourceSync, nil
}

func (s *DummyResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, error) {
	for i, sync := range *s.resourceSyncVals {
		if *resourceSync.Metadata.Name == *sync.Metadata.Name {
			if callbackEvent != nil {
				callbackEvent(ctx, domain.ResourceSyncKind, orgId, lo.FromPtr(resourceSync.Metadata.Name), (*s.resourceSyncVals)[i], resourceSync, false, nil)
			}
			var r domain.ResourceSync
			deepCopy(resourceSync, &r)
			(*s.resourceSyncVals)[i] = r
			return resourceSync, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync, callbackEvent store.EventCallback) (*domain.ResourceSync, bool, error) {
	var oldRs *domain.ResourceSync
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
	var r domain.ResourceSync
	deepCopy(resourceSync, &r)
	*s.resourceSyncVals = append(*s.resourceSyncVals, r)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.ResourceSyncKind, orgId, lo.FromPtr(resourceSync.Metadata.Name), oldRs, resourceSync, created, nil)
	}
	return resourceSync, created, nil
}

func (s *DummyResourceSync) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.ResourceSyncList, error) {
	return &domain.ResourceSyncList{
		ApiVersion: "",
		Kind:       "",
		Metadata:   domain.ListMeta{},
		Items:      *s.resourceSyncVals,
	}, nil
}

func (s *DummyResourceSync) UpdateStatus(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync, eventCallback store.EventCallback) (*domain.ResourceSync, error) {
	for i, rs := range *s.resourceSyncVals {
		if *resourceSync.Metadata.Name == *rs.Metadata.Name {
			var r domain.ResourceSync
			deepCopy(resourceSync, &r)
			(*s.resourceSyncVals)[i].Status = r.Status
			return resourceSync, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> EnrollmentRequest

func (s *DummyEnrollmentRequest) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, error) {
	for _, enrollment := range *s.enrollmentRequests {
		if name == *enrollment.Metadata.Name {
			var e domain.EnrollmentRequest
			deepCopy(enrollment, &e)
			return &e, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyEnrollmentRequest) Create(ctx context.Context, orgId uuid.UUID, er *domain.EnrollmentRequest, callbackEvent store.EventCallback) (*domain.EnrollmentRequest, error) {
	var e domain.EnrollmentRequest
	deepCopy(er, &e)
	*s.enrollmentRequests = append(*s.enrollmentRequests, e)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.EnrollmentRequestKind, orgId, lo.FromPtr(er.Metadata.Name), nil, er, true, nil)
	}
	return er, nil
}

func (s *DummyEnrollmentRequest) CreateWithFromAPI(ctx context.Context, orgId uuid.UUID, er *domain.EnrollmentRequest, fromAPI bool, callbackEvent store.EventCallback) (*domain.EnrollmentRequest, error) {
	return s.Create(ctx, orgId, er, callbackEvent)
}

func (s *DummyEnrollmentRequest) UpdateStatus(ctx context.Context, orgId uuid.UUID, er *domain.EnrollmentRequest, callbackEvent store.EventCallback) (*domain.EnrollmentRequest, error) {
	for i, e := range *s.enrollmentRequests {
		if *er.Metadata.Name == *e.Metadata.Name {
			oldEr := (*s.enrollmentRequests)[i]
			if callbackEvent != nil {
				callbackEvent(ctx, domain.EnrollmentRequestKind, orgId, lo.FromPtr(er.Metadata.Name), oldEr, er, false, nil)
			}
			oldEr.Status = er.Status
			return er, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> Organization

func (s *DummyOrganization) InitialMigration(ctx context.Context) error {
	if s.err != nil {
		return s.err
	}
	return nil
}

func (s *DummyOrganization) Create(ctx context.Context, org *model.Organization) (*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.organizations == nil {
		s.organizations = &[]*model.Organization{}
	}
	*s.organizations = append(*s.organizations, org)
	return org, nil
}

func (s *DummyOrganization) List(ctx context.Context, listParams store.ListParams) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.organizations == nil {
		return []*model.Organization{}, nil
	}
	return *s.organizations, nil
}

// --------------------------------------> WorkerClient

type DummyWorkerClient struct {
}

func (s *DummyWorkerClient) EmitEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	// TODO: implement
}

// --------------------------------------> Helper functions

func deepCopy(src, dst interface{}) {
	data, err := json.Marshal(src)
	if err != nil {
		panic(fmt.Sprintf("deepCopy failed in test: %v", err))
	}
	if err = json.Unmarshal(data, dst); err != nil {
		panic(fmt.Sprintf("deepCopy failed in test: %v", err))
	}
}
