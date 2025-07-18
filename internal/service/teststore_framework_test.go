package service

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/google/uuid"
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
	organizations      *DummyOrganization
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

type DummyOrganization struct {
	store.Organization
	organizations *[]*model.Organization
	err           error
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

func (s *DummyEvent) Create(ctx context.Context, orgId uuid.UUID, event *api.Event) error {
	*s.events = append(*s.events, *event)
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
	for _, dev := range *s.devices {
		if name == *dev.Metadata.Name {
			return &dev, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyDevice) Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback) (*api.Device, api.ResourceUpdatedDetails, error) {
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			oldDevice := (*s.devices)[i]
			*s.devices = append((*s.devices)[:i], (*s.devices)[i+1:]...)
			if validationCallback != nil {
				// TODO
				if err := validationCallback(ctx, &oldDevice, device); err != nil {
					return nil, api.ResourceUpdatedDetails{}, err
				}
			}
			if callback != nil {
				callback(ctx, store.NullOrgId, &oldDevice, device)
			}
			//device.Status.LastSeen = time.Now()
			*s.devices = append(*s.devices, *device)
			return device, api.ResourceUpdatedDetails{}, nil
		}
	}
	return nil, api.ResourceUpdatedDetails{}, flterrors.ErrResourceNotFound

}

func (s *DummyDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback) (*api.Device, bool, api.ResourceUpdatedDetails, error) {
	created := true
	var oldDevice api.Device
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			oldDevice = (*s.devices)[i]
			*s.devices = append((*s.devices)[:i], (*s.devices)[i+1:]...)
			created = false
			break
		}
	}
	details := api.ResourceUpdatedDetails{}
	// TODO: update found device
	if !created {
		if validationCallback != nil {
			if err := validationCallback(ctx, &oldDevice, device); err != nil {
				return nil, created, api.ResourceUpdatedDetails{}, err
			}
		}
		if callback != nil {
			callback(ctx, store.NullOrgId, &oldDevice, device)
		}
	}
	//device.Status.LastSeen = time.Now()
	*s.devices = append(*s.devices, *device)
	return device, created, details, nil
}

func (s *DummyDevice) Create(ctx context.Context, orgId uuid.UUID, device *api.Device, callback store.DeviceStoreCallback) (*api.Device, error) {
	if s.devices == nil {
		s.devices = &[]api.Device{}
	}
	//device.Status.LastSeen = time.Now()
	*s.devices = append(*s.devices, *device)
	return device, nil
}

func (s *DummyDevice) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error) {
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			(*s.devices)[i].Status = device.Status
			return device, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

// --------------------------------------> Fleet

func (s *DummyFleet) Get(ctx context.Context, orgId uuid.UUID, name string, options ...store.GetOption) (*api.Fleet, error) {
	for _, fleet := range *s.fleets {
		if name == *fleet.Metadata.Name {
			return &fleet, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyFleet) Create(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback store.FleetStoreCallback) (*api.Fleet, error) {
	if s.fleets == nil {
		s.fleets = &[]api.Fleet{}
	}
	*s.fleets = append(*s.fleets, *fleet)
	return fleet, nil
}

func (s *DummyFleet) Update(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.FleetStoreCallback) (*api.Fleet, api.ResourceUpdatedDetails, error) {
	for i, flt := range *s.fleets {
		if *fleet.Metadata.Name == *flt.Metadata.Name {
			*s.fleets = append((*s.fleets)[:i], (*s.fleets)[i+1:]...)
			*s.fleets = append(*s.fleets, *fleet)
			return fleet, api.ResourceUpdatedDetails{}, nil
		}
	}
	return nil, api.ResourceUpdatedDetails{}, flterrors.ErrResourceNotFound
}

// --------------------------------------> Repository

func (s *DummyRepository) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error) {
	for _, repo := range *s.repositories {
		if name == *repo.Metadata.Name {
			return &repo, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) Create(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, error) {
	if s.repositories == nil {
		s.repositories = &[]api.Repository{}
	}
	*s.repositories = append(*s.repositories, *repository)
	return repository, nil
}

func (s *DummyRepository) Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, api.ResourceUpdatedDetails, error) {
	for i, repo := range *s.repositories {
		if *repository.Metadata.Name == *repo.Metadata.Name {
			*s.repositories = append((*s.repositories)[:i], (*s.repositories)[i+1:]...)
			*s.repositories = append(*s.repositories, *repository)
			return repository, api.ResourceUpdatedDetails{}, nil
		}
	}
	return nil, api.ResourceUpdatedDetails{}, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, bool, api.ResourceUpdatedDetails, error) {
	return nil, false, api.ResourceUpdatedDetails{}, nil
}

// --------------------------------------> ResourceSync

func (s *DummyResourceSync) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	for _, res := range *s.resourceSyncVals {
		if name == *res.Metadata.Name {
			return &res, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) Create(ctx context.Context, orgId uuid.UUID, rs *api.ResourceSync) (*api.ResourceSync, error) {
	if s.resourceSyncVals == nil {
		s.resourceSyncVals = &[]api.ResourceSync{}
	}
	*s.resourceSyncVals = append(*s.resourceSyncVals, *rs)
	return rs, nil
}

func (s *DummyResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, api.ResourceUpdatedDetails, error) {
	for i, sync := range *s.resourceSyncVals {
		if *resourceSync.Metadata.Name == *sync.Metadata.Name {
			*s.resourceSyncVals = append((*s.resourceSyncVals)[:i], (*s.resourceSyncVals)[i+1:]...)
			*s.resourceSyncVals = append(*s.resourceSyncVals, *resourceSync)
			return resourceSync, api.ResourceUpdatedDetails{}, nil
		}
	}
	return nil, api.ResourceUpdatedDetails{}, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, bool, api.ResourceUpdatedDetails, error) {
	created := true
	//var oldResourceSync api.ResourceSync
	for i, resource := range *s.resourceSyncVals {
		if *resourceSync.Metadata.Name == *resource.Metadata.Name {
			//oldResourceSync = (*s.resourceSyncVals)[i]
			*s.resourceSyncVals = append((*s.resourceSyncVals)[:i], (*s.resourceSyncVals)[i+1:]...)
			created = false
			break
		}
	}
	details := api.ResourceUpdatedDetails{}
	// TODO: update found device
	// resourceSync -> oldResourceSync
	*s.resourceSyncVals = append(*s.resourceSyncVals, *resourceSync)
	return resourceSync, created, details, nil

}

// --------------------------------------> EnrollmentRequest

func (s *DummyEnrollmentRequest) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error) {
	for _, enrollment := range *s.enrollmentRequests {
		if name == *enrollment.Metadata.Name {
			return &enrollment, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyEnrollmentRequest) Create(ctx context.Context, orgId uuid.UUID, rs *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	if s.enrollmentRequests == nil {
		s.enrollmentRequests = &[]api.EnrollmentRequest{}
	}
	*s.enrollmentRequests = append(*s.enrollmentRequests, *rs)
	return rs, nil
}

func (s *DummyEnrollmentRequest) UpdateStatus(ctx context.Context, orgId uuid.UUID, er *api.EnrollmentRequest) (*api.EnrollmentRequest, error) {
	for i, dev := range *s.enrollmentRequests {
		if *er.Metadata.Name == *dev.Metadata.Name {
			oldEr := (*s.enrollmentRequests)[i]
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

func (s *DummyOrganization) List(ctx context.Context) ([]*model.Organization, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.organizations == nil {
		return []*model.Organization{}, nil
	}
	return *s.organizations, nil
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
