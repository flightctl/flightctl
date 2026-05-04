package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
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
	devices                   *DummyDevice
	events                    *DummyEvent
	fleets                    *DummyFleet
	catalogs                  *DummyCatalog
	repositories              *DummyRepository
	resourceSyncVals          *DummyResourceSync
	enrollmentRequests        *DummyEnrollmentRequest
	organizations             *DummyOrganization
	dummyVulnerabilityFinding *DummyVulnerabilityFinding
}

type DummyVulnerabilityFinding struct {
	findings    []model.VulnerabilityFinding
	deviceStore *DummyDevice

	// StubCVELifecycleResponses switches ListCVEEvent* methods to return the fields below instead of empty (for CVE sync tests).
	StubCVELifecycleResponses bool
	CVELifecycleResolution    []store.CVEEventResolutionCandidate
	CVELifecycleResolutionErr error
	CVELifecycleSupersede     []store.CVEEventCandidate
	CVELifecycleSupersedeErr  error
	CVELifecycleCritical      []store.CVEEventCandidate
	CVELifecycleCriticalErr   error
	CVELifecycleWarning       []store.CVEEventCandidate
	CVELifecycleWarningErr    error
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

type DummyCatalog struct {
	store.Catalog
	catalogs *[]domain.Catalog
	items    *[]domain.CatalogItem
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
	if s.catalogs == nil {
		s.catalogs = &DummyCatalog{catalogs: &[]domain.Catalog{}, items: &[]domain.CatalogItem{}}
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
	if s.dummyVulnerabilityFinding == nil {
		s.dummyVulnerabilityFinding = &DummyVulnerabilityFinding{deviceStore: s.devices}
	}
}

func (s *TestStore) Fleet() store.Fleet {
	s.init()
	return s.fleets
}

func (s *TestStore) Catalog() store.Catalog {
	s.init()
	return s.catalogs
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

func (s *TestStore) VulnerabilityFinding() store.VulnerabilityFinding {
	s.init()
	return s.dummyVulnerabilityFinding
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

func (s *DummyDevice) GetWithTimestamp(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
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

func (s *DummyFleet) Delete(ctx context.Context, orgId uuid.UUID, name string, callbackEvent store.EventCallback) error {
	for i, fleet := range *s.fleets {
		if name == *fleet.Metadata.Name {
			var oldFleet domain.Fleet
			deepCopy(fleet, &oldFleet)
			*s.fleets = append((*s.fleets)[:i], (*s.fleets)[i+1:]...)
			if callbackEvent != nil {
				callbackEvent(ctx, domain.FleetKind, orgId, name, &oldFleet, nil, false, nil)
			}
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
}

// --------------------------------------> Catalog

func (s *DummyCatalog) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, error) {
	for _, catalog := range *s.catalogs {
		if name == *catalog.Metadata.Name {
			var c domain.Catalog
			deepCopy(catalog, &c)
			return &c, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyCatalog) Create(ctx context.Context, orgId uuid.UUID, catalog *domain.Catalog, callbackEvent store.EventCallback) (*domain.Catalog, error) {
	var c domain.Catalog
	deepCopy(catalog, &c)
	*s.catalogs = append(*s.catalogs, c)
	if callbackEvent != nil {
		callbackEvent(ctx, domain.CatalogKind, orgId, lo.FromPtr(catalog.Metadata.Name), nil, catalog, true, nil)
	}
	return catalog, nil
}

func (s *DummyCatalog) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.RemoveOwnerCallback, callbackEvent store.EventCallback) error {
	for i, catalog := range *s.catalogs {
		if name == *catalog.Metadata.Name {
			var oldCatalog domain.Catalog
			deepCopy(catalog, &oldCatalog)
			*s.catalogs = append((*s.catalogs)[:i], (*s.catalogs)[i+1:]...)
			if callbackEvent != nil {
				callbackEvent(ctx, domain.CatalogKind, orgId, name, &oldCatalog, nil, false, nil)
			}
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
}

func (s *DummyCatalog) GetItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*domain.CatalogItem, error) {
	for _, item := range *s.items {
		if item.Metadata.Catalog == catalogName && *item.Metadata.Name == itemName {
			var i domain.CatalogItem
			deepCopy(item, &i)
			return &i, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyCatalog) CreateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *domain.CatalogItem) (*domain.CatalogItem, error) {
	var i domain.CatalogItem
	deepCopy(item, &i)
	i.Metadata.Catalog = catalogName
	*s.items = append(*s.items, i)
	return &i, nil
}

func (s *DummyCatalog) CreateOrUpdateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *domain.CatalogItem) (*domain.CatalogItem, bool, error) {
	for idx, existing := range *s.items {
		if existing.Metadata.Catalog == catalogName && *existing.Metadata.Name == *item.Metadata.Name {
			// Update existing item
			var i domain.CatalogItem
			deepCopy(item, &i)
			i.Metadata.Catalog = catalogName
			if i.Metadata.Owner == nil {
				i.Metadata.Owner = existing.Metadata.Owner
			}
			(*s.items)[idx] = i
			return &i, false, nil
		}
	}
	result, err := s.CreateItem(ctx, orgId, catalogName, item)
	return result, true, err
}

func (s *DummyCatalog) UpdateItem(ctx context.Context, orgId uuid.UUID, catalogName string, item *domain.CatalogItem) (*domain.CatalogItem, error) {
	for idx, existing := range *s.items {
		if existing.Metadata.Catalog == catalogName && *existing.Metadata.Name == *item.Metadata.Name {
			var i domain.CatalogItem
			deepCopy(item, &i)
			i.Metadata.Catalog = catalogName
			if i.Metadata.Owner == nil {
				i.Metadata.Owner = existing.Metadata.Owner
			}
			(*s.items)[idx] = i
			return &i, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyCatalog) DeleteItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) error {
	for i, item := range *s.items {
		if item.Metadata.Catalog == catalogName && *item.Metadata.Name == itemName {
			*s.items = append((*s.items)[:i], (*s.items)[i+1:]...)
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
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

// --------------------------------------> VulnerabilityFinding

func (d *DummyVulnerabilityFinding) InitialMigration(_ context.Context) error { return nil }

func (d *DummyVulnerabilityFinding) ListDeployedImageDigests(_ context.Context) ([]string, error) {
	seen := map[string]struct{}{}
	for _, f := range d.findings {
		seen[f.ImageDigest] = struct{}{}
	}
	digests := make([]string, 0, len(seen))
	for digest := range seen {
		digests = append(digests, digest)
	}
	return digests, nil
}

func (d *DummyVulnerabilityFinding) UpsertFindings(_ context.Context, findings []model.VulnerabilityFinding) ([]model.ChangedFinding, error) {
	var changed []model.ChangedFinding
	for _, f := range findings {
		updated := false
		for i, existing := range d.findings {
			if existing.ImageDigest == f.ImageDigest && existing.CveID == f.CveID {
				// Track if severity or status changed
				if string(existing.Severity) != string(f.Severity) || string(existing.Status) != string(f.Status) {
					changed = append(changed, model.ChangedFinding{
						ImageDigest: f.ImageDigest,
						CveID:       f.CveID,
						Severity:    string(f.Severity),
						Status:      string(f.Status),
					})
				}
				d.findings[i] = f
				updated = true
				break
			}
		}
		if !updated {
			// New finding
			changed = append(changed, model.ChangedFinding{
				ImageDigest: f.ImageDigest,
				CveID:       f.CveID,
				Severity:    string(f.Severity),
				Status:      string(f.Status),
			})
			d.findings = append(d.findings, f)
		}
	}
	return changed, nil
}

func (d *DummyVulnerabilityFinding) GetVulnerabilities(ctx context.Context, digest string, listParams store.ListParams) (*store.VulnerabilityListResult, error) {
	filters, err := parseVulnerabilityFindingFilters(ctx, listParams.FieldSelector)
	if err != nil {
		return nil, err
	}

	var matched []model.VulnerabilityFinding
	for _, f := range d.findings {
		if f.ImageDigest != digest {
			continue
		}
		if !findingMatchesVulnerabilityFilters(f, filters) {
			continue
		}
		matched = append(matched, f)
	}

	total := int64(len(matched))
	sortCol, sortOrder := getSortColAndOrder(listParams)
	sort.SliceStable(matched, func(i, j int) bool {
		return compareFindingsBySortColumn(matched[i], matched[j], sortCol, sortOrder) < 0
	})

	if listParams.Limit <= 0 {
		return &store.VulnerabilityListResult{Items: matched}, nil
	}

	// Simple offset-based pagination for tests (cursor ignored for simplicity)
	offset := 0
	if listParams.Continue != nil && len(listParams.Continue.Names) > 0 {
		// For tests, we use a simple index as continue token
		offset, _ = strconv.Atoi(listParams.Continue.Names[0])
	}
	start := offset
	if start > len(matched) {
		start = len(matched)
	}
	end := start + listParams.Limit
	if end > len(matched) {
		end = len(matched)
	}
	items := matched[start:end]

	var cont *string
	if end < len(matched) {
		cont = store.BuildContinueString([]string{strconv.Itoa(end)}, total-int64(end))
	}

	return &store.VulnerabilityListResult{Items: items, Continue: cont}, nil
}

func (d *DummyVulnerabilityFinding) GetVulnerabilitySummary(ctx context.Context, digest string, fieldSelector *selector.FieldSelector) (store.SeverityCounts, error) {
	filters, err := parseVulnerabilityFindingFilters(ctx, fieldSelector)
	if err != nil {
		return store.SeverityCounts{}, err
	}

	seenRanks := make(map[string]int)
	for _, f := range d.findings {
		if f.ImageDigest != digest {
			continue
		}
		if !findingMatchesVulnerabilityFilters(f, filters) {
			continue
		}
		rank := store.SeverityToRank(f.Severity)
		if current, ok := seenRanks[f.CveID]; !ok || rank < current {
			seenRanks[f.CveID] = rank
		}
	}

	var counts store.SeverityCounts
	for _, rank := range seenRanks {
		counts.Total++
		switch model.VulnerabilitySeverityFromRank(rank) {
		case model.VulnerabilitySeverityCritical:
			counts.Critical++
		case model.VulnerabilitySeverityHigh:
			counts.High++
		case model.VulnerabilitySeverityMedium:
			counts.Medium++
		case model.VulnerabilitySeverityLow:
			counts.Low++
		case model.VulnerabilitySeverityNone:
			counts.None++
		default:
			counts.Unknown++
		}
	}
	return counts, nil
}

func (d *DummyVulnerabilityFinding) ListFleetDeviceImageDigests(_ context.Context, _ uuid.UUID, fleetName string) ([]store.DeviceImageDigest, error) {
	owner := util.ResourceOwner(domain.FleetKind, fleetName)
	seen := map[string]store.DeviceImageDigest{}
	for _, dev := range *d.deviceStore.devices {
		if lo.FromPtr(dev.Metadata.Owner) != owner {
			continue
		}
		if dev.Status == nil || dev.Status.Os.ImageDigest == "" {
			continue
		}
		key := dev.Status.Os.ImageDigest
		entry := seen[key]
		entry.ImageDigest = key
		entry.DeviceCount++

		imageFound := false
		for _, imageRef := range entry.ImageRefs {
			if imageRef == dev.Status.Os.Image {
				imageFound = true
				break
			}
		}
		if !imageFound {
			entry.ImageRefs = append(entry.ImageRefs, dev.Status.Os.Image)
		}
		seen[key] = entry
	}
	result := make([]store.DeviceImageDigest, 0, len(seen))
	for _, v := range seen {
		result = append(result, v)
	}
	return result, nil
}

func (d *DummyVulnerabilityFinding) ListOrgDeviceImageDigests(_ context.Context, _ uuid.UUID) ([]store.DeviceImageDigest, error) {
	seen := map[string]store.DeviceImageDigest{}
	for _, dev := range *d.deviceStore.devices {
		if dev.Status == nil || dev.Status.Os.ImageDigest == "" {
			continue
		}
		key := dev.Status.Os.ImageDigest
		entry := seen[key]
		entry.ImageDigest = key
		entry.DeviceCount++

		imageFound := false
		for _, imageRef := range entry.ImageRefs {
			if imageRef == dev.Status.Os.Image {
				imageFound = true
				break
			}
		}
		if !imageFound {
			entry.ImageRefs = append(entry.ImageRefs, dev.Status.Os.Image)
		}
		seen[key] = entry
	}
	result := make([]store.DeviceImageDigest, 0, len(seen))
	for _, v := range seen {
		result = append(result, v)
	}
	return result, nil
}

func (d *DummyVulnerabilityFinding) GetVulnerabilityGroups(ctx context.Context, digests []store.DeviceImageDigest, listParams store.ListParams) (*store.VulnerabilityGroupResult, error) {
	filters, err := parseVulnerabilityFindingFilters(ctx, listParams.FieldSelector)
	if err != nil {
		return nil, err
	}

	digestSet := map[string]struct{}{}
	for _, dig := range digests {
		digestSet[dig.ImageDigest] = struct{}{}
	}
	groupMap := map[string]*store.VulnerabilityGroup{}
	for _, f := range d.findings {
		if _, ok := digestSet[f.ImageDigest]; !ok {
			continue
		}
		if !findingMatchesVulnerabilityFilters(f, filters) {
			continue
		}
		g, exists := groupMap[f.CveID]
		if !exists {
			rank := store.SeverityToRank(f.Severity)
			g = &store.VulnerabilityGroup{CveID: f.CveID, SeverityRank: rank}
			groupMap[f.CveID] = g
		} else {
			rank := store.SeverityToRank(f.Severity)
			if rank < g.SeverityRank {
				g.SeverityRank = rank
			}
		}

		if f.CvssScore != nil && (g.MaxCvssScore == nil || *f.CvssScore > *g.MaxCvssScore) {
			score := *f.CvssScore
			g.MaxCvssScore = &score
		}
		if f.PublishedAt != nil && (g.MaxPublishedAt == nil || f.PublishedAt.After(*g.MaxPublishedAt)) {
			publishedAt := *f.PublishedAt
			g.MaxPublishedAt = &publishedAt
		}
		g.Findings = append(g.Findings, f)
	}

	groups := make([]store.VulnerabilityGroup, 0, len(groupMap))
	for _, g := range groupMap {
		groups = append(groups, *g)
	}
	sortCol, sortOrder := getSortColAndOrder(listParams)
	sort.SliceStable(groups, func(i, j int) bool {
		return compareVulnerabilityGroupsBySortColumn(groups[i], groups[j], sortCol, sortOrder) < 0
	})

	total := int64(len(groups))
	if listParams.Limit <= 0 {
		return &store.VulnerabilityGroupResult{Groups: groups}, nil
	}

	// Simple offset-based pagination for tests
	offset := 0
	if listParams.Continue != nil && len(listParams.Continue.Names) > 0 {
		offset, _ = strconv.Atoi(listParams.Continue.Names[0])
	}
	start := offset
	if start > len(groups) {
		start = len(groups)
	}
	end := start + listParams.Limit
	if end > len(groups) {
		end = len(groups)
	}
	items := groups[start:end]

	var cont *string
	if end < len(groups) {
		cont = store.BuildContinueString([]string{strconv.Itoa(end)}, total-int64(end))
	}

	return &store.VulnerabilityGroupResult{Groups: items, Continue: cont}, nil
}

func (d *DummyVulnerabilityFinding) GetFleetVulnerabilitySummary(ctx context.Context, digests []store.DeviceImageDigest, fieldSelector *selector.FieldSelector) (store.FleetSeverityCounts, error) {
	filters, err := parseVulnerabilityFindingFilters(ctx, fieldSelector)
	if err != nil {
		return store.FleetSeverityCounts{}, err
	}

	digestSet := map[string]struct{}{}
	for _, dig := range digests {
		digestSet[dig.ImageDigest] = struct{}{}
	}
	// Track the worst (minimum rank) severity per CVE across all matching digests.
	cveWorstRank := map[string]int{}
	for _, f := range d.findings {
		if _, ok := digestSet[f.ImageDigest]; !ok {
			continue
		}
		if !findingMatchesVulnerabilityFilters(f, filters) {
			continue
		}
		rank := store.SeverityToRank(f.Severity)
		if existing, seen := cveWorstRank[f.CveID]; !seen || rank < existing {
			cveWorstRank[f.CveID] = rank
		}
	}
	var counts store.FleetSeverityCounts
	counts.UniqueDigests = int64(len(digestSet))
	for _, rank := range cveWorstRank {
		counts.Total++
		switch model.VulnerabilitySeverityFromRank(rank) {
		case model.VulnerabilitySeverityCritical:
			counts.Critical++
		case model.VulnerabilitySeverityHigh:
			counts.High++
		case model.VulnerabilitySeverityMedium:
			counts.Medium++
		case model.VulnerabilitySeverityLow:
			counts.Low++
		case model.VulnerabilitySeverityNone:
			counts.None++
		default:
			counts.Unknown++
		}
	}
	return counts, nil
}

func (d *DummyVulnerabilityFinding) GetOrgVulnerabilitySummary(_ context.Context, _ uuid.UUID) (store.OrgVulnerabilitySummary, error) {
	return store.OrgVulnerabilitySummary{}, nil
}

func (d *DummyVulnerabilityFinding) FindAnyFindingForCVE(_ context.Context, cveID string) (*model.VulnerabilityFinding, error) {
	for i := range d.findings {
		if d.findings[i].CveID == cveID {
			f := d.findings[i]
			return &f, nil
		}
	}
	return nil, nil
}

func (d *DummyVulnerabilityFinding) FindingsForCVEAndImageDigests(_ context.Context, cveID string, imageDigests []string) ([]model.VulnerabilityFinding, error) {
	if len(imageDigests) == 0 {
		return nil, nil
	}
	wantDigest := map[string]struct{}{}
	for _, dig := range imageDigests {
		wantDigest[dig] = struct{}{}
	}
	var out []model.VulnerabilityFinding
	for _, f := range d.findings {
		if f.CveID != cveID {
			continue
		}
		if _, ok := wantDigest[f.ImageDigest]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func (d *DummyVulnerabilityFinding) GetVulnerabilityImpactPage(_ context.Context, _ uuid.UUID, _ string, _ store.ListParams) (*store.ImpactPageResult, error) {
	return &store.ImpactPageResult{}, nil
}

func (d *DummyVulnerabilityFinding) GetImpactDigestDetails(_ context.Context, _ uuid.UUID, _ string, _ []string, _ bool) ([]store.ImpactDigestDetail, error) {
	return nil, nil
}

func (d *DummyVulnerabilityFinding) ListCVEEventResolutionCandidates(_ context.Context) ([]store.CVEEventResolutionCandidate, error) {
	if d.StubCVELifecycleResponses {
		return d.CVELifecycleResolution, d.CVELifecycleResolutionErr
	}
	return nil, nil
}

func (d *DummyVulnerabilityFinding) ListOpenWarningSupersedeCVEEventCandidates(_ context.Context) ([]store.CVEEventCandidate, error) {
	if d.StubCVELifecycleResponses {
		return d.CVELifecycleSupersede, d.CVELifecycleSupersedeErr
	}
	return nil, nil
}

func (d *DummyVulnerabilityFinding) ListCriticalCVEEventCandidates(_ context.Context) ([]store.CVEEventCandidate, error) {
	if d.StubCVELifecycleResponses {
		return d.CVELifecycleCritical, d.CVELifecycleCriticalErr
	}
	return nil, nil
}

func (d *DummyVulnerabilityFinding) ListWarningCVEEventCandidates(_ context.Context) ([]store.CVEEventCandidate, error) {
	if d.StubCVELifecycleResponses {
		return d.CVELifecycleWarning, d.CVELifecycleWarningErr
	}
	return nil, nil
}

func (d *DummyVulnerabilityFinding) ComputeAllCVEActions(_ context.Context, _ []model.ChangedFinding, _ time.Time) ([]model.CVEEventAction, error) {
	return nil, nil
}

func (d *DummyVulnerabilityFinding) BatchUpdateOpenEvents(_ context.Context, actions []model.CVEEventAction) ([]model.CVEEventAction, error) {
	return actions, nil
}

type vulnerabilityFindingFilter struct {
	field string
	op    string
	value string
}

var vulnerabilityFilterPattern = regexp.MustCompile(`\b(severity|cve_id)\b\s*(=|!=)\s*\?`)

func parseVulnerabilityFindingFilters(ctx context.Context, fs *selector.FieldSelector) ([]vulnerabilityFindingFilter, error) {
	if fs == nil {
		return nil, nil
	}

	query, args, err := fs.Parse(ctx, &dummyVulnerabilityFindingResolver{})
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, nil
	}

	matches := vulnerabilityFilterPattern.FindAllStringSubmatch(query, -1)
	if len(matches) != len(args) {
		return nil, fmt.Errorf("unsupported field selector query for dummy vulnerability store")
	}

	filters := make([]vulnerabilityFindingFilter, 0, len(matches))
	for i, m := range matches {
		value, ok := args[i].(string)
		if !ok {
			return nil, fmt.Errorf("unsupported field selector argument type %T for dummy vulnerability store", args[i])
		}
		filters = append(filters, vulnerabilityFindingFilter{
			field: m[1],
			op:    m[2],
			value: value,
		})
	}
	return filters, nil
}

func findingMatchesVulnerabilityFilters(f model.VulnerabilityFinding, filters []vulnerabilityFindingFilter) bool {
	for _, filter := range filters {
		var actual string
		switch filter.field {
		case "severity":
			actual = string(f.Severity)
		case "cve_id":
			actual = f.CveID
		default:
			return false
		}

		switch filter.op {
		case "=":
			if actual != filter.value {
				return false
			}
		case "!=":
			if actual == filter.value {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func getSortColAndOrder(listParams store.ListParams) (store.SortColumn, store.SortOrder) {
	sortCol := store.SortBySeverity
	if len(listParams.SortColumns) > 0 {
		sortCol = listParams.SortColumns[0]
	}
	sortOrder := store.SortDesc
	if listParams.SortOrder != nil {
		sortOrder = *listParams.SortOrder
	}
	return sortCol, sortOrder
}

func compareFindingsBySortColumn(a, b model.VulnerabilityFinding, sortCol store.SortColumn, sortOrder store.SortOrder) int {
	descending := sortOrder == store.SortDesc
	switch sortCol {
	case store.SortByCvssScore:
		if result := compareNullableFloat(a.CvssScore, b.CvssScore, descending); result != 0 {
			return result
		}
		return strings.Compare(a.CveID, b.CveID)
	case store.SortByPublishedAt:
		if result := compareNullableTime(a.PublishedAt, b.PublishedAt, descending); result != 0 {
			return result
		}
		return strings.Compare(a.CveID, b.CveID)
	case store.SortByCveId:
		if descending {
			return strings.Compare(b.CveID, a.CveID)
		}
		return strings.Compare(a.CveID, b.CveID)
	default: // severity
		rankA := store.SeverityToRank(a.Severity)
		rankB := store.SeverityToRank(b.Severity)
		if rankA != rankB {
			if descending {
				if rankA < rankB {
					return -1
				}
				return 1
			}
			if rankA > rankB {
				return -1
			}
			return 1
		}
		return strings.Compare(a.CveID, b.CveID)
	}
}

func compareVulnerabilityGroupsBySortColumn(a, b store.VulnerabilityGroup, sortCol store.SortColumn, sortOrder store.SortOrder) int {
	descending := sortOrder == store.SortDesc
	switch sortCol {
	case store.SortByCvssScore:
		if result := compareNullableFloat(a.MaxCvssScore, b.MaxCvssScore, descending); result != 0 {
			return result
		}
		return strings.Compare(a.CveID, b.CveID)
	case store.SortByPublishedAt:
		if result := compareNullableTime(a.MaxPublishedAt, b.MaxPublishedAt, descending); result != 0 {
			return result
		}
		return strings.Compare(a.CveID, b.CveID)
	case store.SortByCveId:
		if descending {
			return strings.Compare(b.CveID, a.CveID)
		}
		return strings.Compare(a.CveID, b.CveID)
	default: // severity
		if a.SeverityRank != b.SeverityRank {
			if descending {
				if a.SeverityRank < b.SeverityRank {
					return -1
				}
				return 1
			}
			if a.SeverityRank > b.SeverityRank {
				return -1
			}
			return 1
		}
		return strings.Compare(a.CveID, b.CveID)
	}
}

func compareNullableFloat(a, b *float64, descending bool) int {
	if a == nil && b == nil {
		return 0
	}
	// Match DB behavior where NULLS are always last.
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	if *a == *b {
		return 0
	}
	if descending {
		if *a > *b {
			return -1
		}
		return 1
	}
	if *a < *b {
		return -1
	}
	return 1
}

func compareNullableTime(a, b *time.Time, descending bool) int {
	if a == nil && b == nil {
		return 0
	}
	// Match DB behavior where NULLS are always last.
	if a == nil {
		return 1
	}
	if b == nil {
		return -1
	}
	if a.Equal(*b) {
		return 0
	}
	if descending {
		if a.After(*b) {
			return -1
		}
		return 1
	}
	if a.Before(*b) {
		return -1
	}
	return 1
}

type dummyVulnerabilityFindingResolver struct{}

func (r *dummyVulnerabilityFindingResolver) ResolveNames(name selector.SelectorName) ([]string, error) {
	switch name.String() {
	case "severity":
		return []string{"severity"}, nil
	case "cveId":
		return []string{"cve_id"}, nil
	default:
		return nil, fmt.Errorf("unsupported field selector %q for dummy vulnerability findings, supported: severity, cveId", name.String())
	}
}

func (r *dummyVulnerabilityFindingResolver) ResolveFields(name selector.SelectorName) ([]*selector.SelectorField, error) {
	switch name.String() {
	case "severity":
		return []*selector.SelectorField{{
			Name:      selector.NewSelectorName("severity"),
			Type:      selector.String,
			FieldName: "severity",
			FieldType: "text",
		}}, nil
	case "cveId":
		return []*selector.SelectorField{{
			Name:      selector.NewSelectorName("cveId"),
			Type:      selector.String,
			FieldName: "cve_id",
			FieldType: "text",
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported field selector %q for dummy vulnerability findings, supported: severity, cveId", name.String())
	}
}

func (r *dummyVulnerabilityFindingResolver) List() []selector.SelectorName {
	return []selector.SelectorName{
		selector.NewSelectorName("severity"),
		selector.NewSelectorName("cveId"),
	}
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
