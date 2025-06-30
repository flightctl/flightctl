package testutils

import (
	"context"
	"net/http"
	"slices"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const StatusSuccessCode = int32(http.StatusOK)
const StatusCreatedCode = int32(http.StatusCreated)
const StatusFailedCode = int32(http.StatusInternalServerError)
const StatusBadRequestCode = int32(http.StatusBadRequest)
const StatusNotFoundCode = int32(http.StatusNotFound)

type TestServiceHandler struct {
	Store store.Store
	log   logrus.FieldLogger
}

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
			if validationCallback != nil {
				// TODO
				if err := validationCallback(ctx, &oldDevice, device); err != nil {
					return nil, api.ResourceUpdatedDetails{}, err
				}
			}
			if callback != nil {
				callback(ctx, store.NullOrgId, &oldDevice, device)
			}
			(*s.devices)[i] = *device
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
	*s.devices = append(*s.devices, *device)
	return device, nil
}

func (s *DummyDevice) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error) {
	for i, dev := range *s.devices {
		if *device.Metadata.Name == *dev.Metadata.Name {
			oldDevice := (*s.devices)[i]
			oldDevice.Status = device.Status
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
	*s.fleets = append(*s.fleets, *fleet)
	return fleet, nil
}

func (s *DummyFleet) Update(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.FleetStoreCallback) (*api.Fleet, api.ResourceUpdatedDetails, error) {
	for i, flt := range *s.fleets {
		if *fleet.Metadata.Name == *flt.Metadata.Name {
			(*s.fleets)[i] = *fleet
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
	*s.repositories = append(*s.repositories, *repository)
	return repository, nil
}

func (s *DummyRepository) Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, api.ResourceUpdatedDetails, error) {
	for i, repo := range *s.repositories {
		if *repository.Metadata.Name == *repo.Metadata.Name {
			(*s.repositories)[i] = *repository
			return repository, api.ResourceUpdatedDetails{}, nil
		}
	}
	return nil, api.ResourceUpdatedDetails{}, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, bool, api.ResourceUpdatedDetails, error) {
	return nil, false, api.ResourceUpdatedDetails{}, nil
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
			return &res, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.RemoveOwnerCallback) error {
	originalLen := len(*s.resourceSyncVals)
	*s.resourceSyncVals = slices.DeleteFunc(*s.resourceSyncVals, func(res api.ResourceSync) bool {
		return name == *res.Metadata.Name
	})
	if len(*s.resourceSyncVals) == originalLen {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

func (s *DummyResourceSync) Create(ctx context.Context, orgId uuid.UUID, rs *api.ResourceSync) (*api.ResourceSync, error) {
	*s.resourceSyncVals = append(*s.resourceSyncVals, *rs)
	return rs, nil
}

func (s *DummyResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, api.ResourceUpdatedDetails, error) {
	for i, sync := range *s.resourceSyncVals {
		if *resourceSync.Metadata.Name == *sync.Metadata.Name {
			(*s.resourceSyncVals)[i] = *resourceSync
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
			*s.resourceSyncVals = append((*s.resourceSyncVals)[:i], (*s.resourceSyncVals)[i+1:]...)
			created = false
			break
		}
	}
	details := api.ResourceUpdatedDetails{}
	// TODO: update found device
	*s.resourceSyncVals = append(*s.resourceSyncVals, *resourceSync)
	return resourceSync, created, details, nil

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
			(*s.resourceSyncVals)[i].Status = resourceSync.Status
			return resourceSync, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
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

// --------------------------------------> CallbackManager

type dummyPublisher struct{}

func (d *dummyPublisher) Publish(_ context.Context, _ []byte) error {
	return nil
}

func (d *dummyPublisher) Close() {}

func DummyCallbackManager() tasks_client.CallbackManager {
	return tasks_client.NewCallbackManager(&dummyPublisher{}, logrus.New())
}

// --------------------------------------> ServiceHandler

func NewTestServiceHandler(log logrus.FieldLogger) *TestServiceHandler {
	return &TestServiceHandler{
		Store: &TestStore{},
		log:   log,
	}
}

type OutcomeFailureFunc func() string

func (s *TestServiceHandler) ReplaceRepositoryStatus(ctx context.Context, name string, repository api.Repository) (*api.Repository, api.Status) {
	repo, _, _ := s.Store.Repository().Update(ctx, store.NullOrgId, &repository, nil)
	return repo, api.StatusOK()
}

func (s *TestServiceHandler) ListRepositories(ctx context.Context, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status) {
	list, _ := s.Store.Repository().List(ctx, store.NullOrgId, store.ListParams{})
	return list, api.StatusOK()
}

func (s *TestServiceHandler) CreateResourceSync(ctx context.Context, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	sync, _ := s.Store.ResourceSync().Create(ctx, store.NullOrgId, &rs)
	return sync, api.StatusCreated()
}

func (s *TestServiceHandler) ListResourceSyncs(ctx context.Context, params api.ListResourceSyncsParams) (*api.ResourceSyncList, api.Status) {
	sync, _ := s.Store.ResourceSync().List(ctx, store.NullOrgId, store.ListParams{})
	return sync, api.StatusOK()
}

func (s *TestServiceHandler) ReplaceResourceSyncStatus(ctx context.Context, name string, resourceSync api.ResourceSync) (*api.ResourceSync, api.Status) {
	rs, err := s.Store.ResourceSync().UpdateStatus(ctx, store.NullOrgId, &resourceSync)
	if err != nil {
		return rs, api.StatusResourceNotFound(api.ResourceSyncKind, name)
	}
	return rs, api.StatusOK()
}

func (s *TestServiceHandler) DeleteResourceSync(ctx context.Context, name string) api.Status {
	err := s.Store.ResourceSync().Delete(ctx, store.NullOrgId, name, nil)
	if err != nil {
		return api.StatusResourceNotFound(api.ResourceSyncKind, name)
	}
	return api.StatusOK()
}

func (s *TestServiceHandler) GetRepository(ctx context.Context, name string) (*api.Repository, api.Status) {
	repo, _ := s.Store.Repository().Get(ctx, store.NullOrgId, name)
	return repo, api.StatusOK()
}

func (s *TestServiceHandler) CreateRepository(ctx context.Context, repo api.Repository) (*api.Repository, api.Status) {
	r, _ := s.Store.Repository().Create(ctx, store.NullOrgId, &repo, nil)
	return r, api.StatusCreated()
}

func (s *TestServiceHandler) ListEvents(ctx context.Context, params api.ListEventsParams) (*api.EventList, api.Status) {
	list, _ := s.Store.Event().List(ctx, store.NullOrgId, store.ListParams{})
	return list, api.StatusOK()
}

func (s *TestServiceHandler) GetFleet(ctx context.Context, name string, params api.GetFleetParams) (*api.Fleet, api.Status) {
	fleet, err := s.Store.Fleet().Get(ctx, store.NullOrgId, name)
	if err != nil {
		return nil, api.StatusResourceNotFound(api.FleetKind, util.DefaultIfNil(&name, "none"))
	}
	return fleet, api.StatusOK()
}

func (s *TestServiceHandler) ListFleets(ctx context.Context, params api.ListFleetsParams) (*api.FleetList, api.Status) {
	fleets, err := s.Store.Fleet().List(ctx, store.NullOrgId, store.ListParams{})
	if err != nil {
		return nil, api.StatusResourceNotFound(api.FleetKind, "none")
	}
	return fleets, api.StatusOK()
}

func (s *TestServiceHandler) ListCertificateSigningRequests(ctx context.Context, params api.ListCertificateSigningRequestsParams) (*api.CertificateSigningRequestList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) CreateCertificateSigningRequest(ctx context.Context, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) DeleteCertificateSigningRequest(ctx context.Context, name string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) PatchCertificateSigningRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.CertificateSigningRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UpdateCertificateSigningRequestApproval(ctx context.Context, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) CreateDevice(ctx context.Context, device api.Device) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ListDevices(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DeviceList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ListDevicesByServiceCondition(ctx context.Context, conditionType string, conditionStatus string, listParams store.ListParams) (*api.DeviceList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UpdateDevice(ctx context.Context, name string, device api.Device, fieldsToUnset []string) (*api.Device, error) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetDevice(ctx context.Context, name string) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceDevice(ctx context.Context, name string, device api.Device, fieldsToUnset []string) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) DeleteDevice(ctx context.Context, name string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetDeviceStatus(ctx context.Context, name string) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceDeviceStatus(ctx context.Context, name string, device api.Device) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) PatchDeviceStatus(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetRenderedDevice(ctx context.Context, name string, params api.GetRenderedDeviceParams) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) PatchDevice(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) DecommissionDevice(ctx context.Context, name string, decom api.DeviceDecommission) (*api.Device, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UpdateDeviceAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UpdateRenderedDevice(ctx context.Context, name, renderedConfig, renderedApplications string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) SetDeviceServiceConditions(ctx context.Context, name string, conditions []api.Condition) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) OverwriteDeviceRepositoryRefs(ctx context.Context, name string, repositoryNames ...string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetDeviceRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) CountDevices(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (int64, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UnmarkDevicesRolloutSelection(ctx context.Context, fleetName string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) MarkDevicesRolloutSelection(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, limit *int) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetDeviceCompletionCounts(ctx context.Context, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) CountDevicesByLabels(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, groupBy []string) ([]map[string]any, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetDevicesSummary(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DevicesSummary, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UpdateDeviceSummaryStatusBatch(ctx context.Context, deviceNames []string, status api.DeviceSummaryStatusType, statusInfo string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UpdateServiceSideDeviceStatus(ctx context.Context, device api.Device) bool {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetEnrollmentConfig(ctx context.Context, params api.GetEnrollmentConfigParams) (*api.EnrollmentConfig, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) CreateEnrollmentRequest(ctx context.Context, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ListEnrollmentRequests(ctx context.Context, params api.ListEnrollmentRequestsParams) (*api.EnrollmentRequestList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetEnrollmentRequest(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) PatchEnrollmentRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.EnrollmentRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) DeleteEnrollmentRequest(ctx context.Context, name string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetEnrollmentRequestStatus(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ApproveEnrollmentRequest(ctx context.Context, name string, approval api.EnrollmentRequestApproval) (*api.EnrollmentRequestApprovalStatus, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) CreateFleet(ctx context.Context, fleet api.Fleet) (*api.Fleet, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceFleet(ctx context.Context, name string, fleet api.Fleet) (*api.Fleet, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) DeleteFleet(ctx context.Context, name string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetFleetStatus(ctx context.Context, name string) (*api.Fleet, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceFleetStatus(ctx context.Context, name string, fleet api.Fleet) (*api.Fleet, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) PatchFleet(ctx context.Context, name string, patch api.PatchRequest) (*api.Fleet, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ListFleetRolloutDeviceSelection(ctx context.Context) (*api.FleetList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ListDisruptionBudgetFleets(ctx context.Context) (*api.FleetList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UpdateFleetConditions(ctx context.Context, name string, conditions []api.Condition) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) UpdateFleetAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) OverwriteFleetRepositoryRefs(ctx context.Context, name string, repositoryNames ...string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetFleetRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ListLabels(ctx context.Context, params api.ListLabelsParams) (*api.LabelList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceRepository(ctx context.Context, name string, repo api.Repository) (*api.Repository, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) DeleteRepository(ctx context.Context, name string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) PatchRepository(ctx context.Context, name string, patch api.PatchRequest) (*api.Repository, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetRepositoryFleetReferences(ctx context.Context, name string) (*api.FleetList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetRepositoryDeviceReferences(ctx context.Context, name string) (*api.DeviceList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetResourceSync(ctx context.Context, name string) (*api.ResourceSync, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ReplaceResourceSync(ctx context.Context, name string, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) PatchResourceSync(ctx context.Context, name string, patch api.PatchRequest) (*api.ResourceSync, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) CreateTemplateVersion(ctx context.Context, tv api.TemplateVersion, immediateRollout bool) (*api.TemplateVersion, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) ListTemplateVersions(ctx context.Context, fleet string, params api.ListTemplateVersionsParams) (*api.TemplateVersionList, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetTemplateVersion(ctx context.Context, fleet string, name string) (*api.TemplateVersion, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) DeleteTemplateVersion(ctx context.Context, fleet string, name string) api.Status {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetLatestTemplateVersion(ctx context.Context, fleet string) (*api.TemplateVersion, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) CreateEvent(ctx context.Context, event *api.Event) {
	_ = s.Store.Event().Create(ctx, store.NullOrgId, event)
}

func (s *TestServiceHandler) DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, api.Status) {
	//TODO implement me
	panic("implement me")
}

func (s *TestServiceHandler) SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) api.Status {
	//TODO implement me
	panic("implement me")
}
func (s *TestServiceHandler) GetDatabaseTime(ctx context.Context) (time.Time, api.Status) {
	//TODO implement me
	panic("implement me")
}
