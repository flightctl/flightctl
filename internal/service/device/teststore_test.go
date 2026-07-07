package device

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// deepCopyDevice mirrors internal/service/teststore_framework_test.go's deepCopy helper,
// scoped to *domain.Device. internal/service/device cannot reuse that helper (or TestStore)
// directly - it is unexported and defined in a _test.go file in a different package - so this
// is a lightweight, package-local equivalent.
func deepCopyDevice(src *domain.Device) *domain.Device {
	if src == nil {
		return nil
	}
	data, err := json.Marshal(src)
	if err != nil {
		panic(fmt.Sprintf("deepCopyDevice failed in test: %v", err))
	}
	dst := &domain.Device{}
	if err := json.Unmarshal(data, dst); err != nil {
		panic(fmt.Sprintf("deepCopyDevice failed in test: %v", err))
	}
	return dst
}

// fakeStore is a minimal hand-written store.Store test double for this package's own tests.
// It embeds the real store.Store interface so every accessor other than Device()/Fleet()
// (which this story's tests need) panics if a test path accidentally reaches it - there is no
// production code path in DeviceServiceHandler that should ever call another accessor.
type fakeStore struct {
	store.Store
	device *fakeDeviceStore
	fleet  *fakeFleetStore
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		device: &fakeDeviceStore{devices: map[string]*domain.Device{}, repoRefs: map[string][]string{}},
		fleet:  &fakeFleetStore{fleets: map[string]*domain.Fleet{}},
	}
}

func (s *fakeStore) Device() store.Device { return s.device }
func (s *fakeStore) Fleet() store.Fleet   { return s.fleet }

// fakeDeviceStore is a minimal in-memory stand-in for store.Device, implementing only the
// methods this package's handler_test.go exercises.
type fakeDeviceStore struct {
	store.Device
	devices  map[string]*domain.Device
	repoRefs map[string][]string
}

func (s *fakeDeviceStore) Create(ctx context.Context, orgId uuid.UUID, device *domain.Device, eventCallback store.EventCallback) (*domain.Device, error) {
	name := lo.FromPtr(device.Metadata.Name)
	if _, exists := s.devices[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	d := deepCopyDevice(device)
	s.devices[name] = d
	if eventCallback != nil {
		eventCallback(ctx, domain.DeviceKind, orgId, name, nil, d, true, nil)
	}
	return deepCopyDevice(d), nil
}

func (s *fakeDeviceStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
	d, ok := s.devices[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return deepCopyDevice(d), nil
}

func (s *fakeDeviceStore) GetWithTimestamp(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
	return s.Get(ctx, orgId, name)
}

func (s *fakeDeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, eventCallback store.EventCallback) (*domain.Device, bool, error) {
	name := lo.FromPtr(device.Metadata.Name)
	old, existed := s.devices[name]
	if existed && validationCallback != nil {
		if err := validationCallback(ctx, old, device); err != nil {
			return nil, false, err
		}
	}
	d := deepCopyDevice(device)
	s.devices[name] = d
	created := !existed
	if eventCallback != nil {
		eventCallback(ctx, domain.DeviceKind, orgId, name, old, d, created, nil)
	}
	return deepCopyDevice(d), created, nil
}

func (s *fakeDeviceStore) Update(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, eventCallback store.EventCallback) (*domain.Device, error) {
	name := lo.FromPtr(device.Metadata.Name)
	old, ok := s.devices[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	if validationCallback != nil {
		if err := validationCallback(ctx, old, device); err != nil {
			return nil, err
		}
	}
	d := deepCopyDevice(device)
	s.devices[name] = d
	if eventCallback != nil {
		eventCallback(ctx, domain.DeviceKind, orgId, name, old, d, false, nil)
	}
	return deepCopyDevice(d), nil
}

func (s *fakeDeviceStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) (bool, error) {
	old, ok := s.devices[name]
	if !ok {
		return false, flterrors.ErrResourceNotFound
	}
	delete(s.devices, name)
	if eventCallback != nil {
		eventCallback(ctx, domain.DeviceKind, orgId, name, old, nil, false, nil)
	}
	return true, nil
}

func (s *fakeDeviceStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *domain.Device, eventCallback store.EventCallback) (*domain.Device, error) {
	name := lo.FromPtr(device.Metadata.Name)
	old := s.devices[name]
	d := deepCopyDevice(device)
	s.devices[name] = d
	if eventCallback != nil {
		eventCallback(ctx, domain.DeviceKind, orgId, name, old, d, false, nil)
	}
	return deepCopyDevice(d), nil
}

func (s *fakeDeviceStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	if _, ok := s.devices[name]; !ok {
		return flterrors.ErrResourceNotFound
	}
	s.repoRefs[name] = repositoryNames
	return nil
}

func (s *fakeDeviceStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, error) {
	if _, ok := s.devices[name]; !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	items := make([]domain.Repository, 0, len(s.repoRefs[name]))
	for _, n := range s.repoRefs[name] {
		items = append(items, domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr(n)}})
	}
	return &domain.RepositoryList{Items: items}, nil
}

func (s *fakeDeviceStore) ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams store.ListParams) (*domain.DeviceList, error) {
	return &domain.DeviceList{}, nil
}

func (s *fakeDeviceStore) List(ctx context.Context, orgId uuid.UUID, listParams store.DeviceListParams) (*domain.DeviceList, error) {
	items := make([]domain.Device, 0, len(s.devices))
	for _, d := range s.devices {
		items = append(items, *deepCopyDevice(d))
	}
	return &domain.DeviceList{Items: items}, nil
}

func (s *fakeDeviceStore) ListConnectivityChanged(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, cutoffTime time.Time) (*domain.DeviceList, error) {
	return &domain.DeviceList{}, nil
}

func (s *fakeDeviceStore) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return int64(len(s.devices)), nil
}

func (s *fakeDeviceStore) CountByLabels(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, groupBy []string) ([]map[string]any, error) {
	return []map[string]any{}, nil
}

func (s *fakeDeviceStore) Labels(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (domain.LabelList, error) {
	return domain.LabelList{}, nil
}

func (s *fakeDeviceStore) Summary(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.DevicesSummary, error) {
	return &domain.DevicesSummary{}, nil
}

func (s *fakeDeviceStore) CompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]domain.DeviceCompletionCount, error) {
	return []domain.DeviceCompletionCount{}, nil
}

func (s *fakeDeviceStore) UnmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) error {
	return nil
}

func (s *fakeDeviceStore) MarkRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, limit *int) error {
	return nil
}

func (s *fakeDeviceStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	d, ok := s.devices[name]
	if !ok {
		return flterrors.ErrResourceNotFound
	}
	current := map[string]string{}
	if d.Metadata.Annotations != nil {
		for k, v := range *d.Metadata.Annotations {
			current[k] = v
		}
	}
	for _, k := range deleteKeys {
		delete(current, k)
	}
	for k, v := range annotations {
		current[k] = v
	}
	d.Metadata.Annotations = &current
	return nil
}

func (s *fakeDeviceStore) UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string, configFingerprints []domain.DependencySyncConfigRefStatus) (string, error) {
	if _, ok := s.devices[name]; !ok {
		return "", flterrors.ErrResourceNotFound
	}
	// Always report "no change in rendered version" so DeviceServiceHandler.UpdateRenderedDevice
	// takes its early-return path and never reaches the rendered.Bus process-global singleton,
	// which requires integration-level initialization (see test/integration/service) and is
	// out of scope for this package's hermetic unit tests.
	return "", nil
}

func (s *fakeDeviceStore) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*domain.Device, error) {
	return s.Get(ctx, orgId, name)
}

func (s *fakeDeviceStore) GetLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*time.Time, error) {
	if _, ok := s.devices[name]; !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return nil, nil
}

func (s *fakeDeviceStore) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
	return nil
}

func (s *fakeDeviceStore) DecommissionDevice(ctx context.Context, orgId uuid.UUID, name string, decom domain.DeviceDecommission, eventCallback store.EventCallback) (*domain.Device, error) {
	d, ok := s.devices[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	old := deepCopyDevice(d)
	if d.Spec == nil {
		d.Spec = &domain.DeviceSpec{}
	}
	d.Spec.Decommissioning = &decom
	if eventCallback != nil {
		eventCallback(ctx, domain.DeviceKind, orgId, name, old, d, false, nil)
	}
	return deepCopyDevice(d), nil
}

func (s *fakeDeviceStore) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, callback store.ServiceConditionsCallback) error {
	d, ok := s.devices[name]
	if !ok {
		return flterrors.ErrResourceNotFound
	}
	if d.Status == nil {
		d.Status = lo.ToPtr(domain.NewDeviceStatus())
	}
	oldConditions := d.Status.Conditions
	d.Status.Conditions = conditions
	if callback != nil {
		callback(ctx, orgId, d, oldConditions, conditions)
	}
	return nil
}

func (s *fakeDeviceStore) RemoveConflictPausedAnnotation(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, []string, error) {
	var ids []string
	for name, d := range s.devices {
		if d.Metadata.Annotations == nil {
			continue
		}
		if v, ok := (*d.Metadata.Annotations)[domain.DeviceAnnotationConflictPaused]; ok && v == "true" {
			newAnnotations := map[string]string{}
			for k, val := range *d.Metadata.Annotations {
				if k != domain.DeviceAnnotationConflictPaused {
					newAnnotations[k] = val
				}
			}
			d.Metadata.Annotations = &newAnnotations
			ids = append(ids, name)
		}
	}
	return int64(len(ids)), ids, nil
}

// fakeFleetStore is a minimal stand-in for store.Fleet, implementing only Get - the single
// call site common.UpdateServiceSideStatus reaches for managed-device status computation.
type fakeFleetStore struct {
	store.Fleet
	fleets   map[string]*domain.Fleet
	getCalls int
}

func (s *fakeFleetStore) Get(ctx context.Context, orgId uuid.UUID, name string, options ...store.GetOption) (*domain.Fleet, error) {
	s.getCalls++
	f, ok := s.fleets[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return f, nil
}

// fakeEvents is a minimal stand-in for events.Service, recording the CreateEvent calls this
// package's tests need to assert on. All other methods are satisfied by the embedded nil
// interface and are not expected to be called by any test in this package.
type fakeEvents struct {
	events.Service
	created []*domain.Event
}

func (f *fakeEvents) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	f.created = append(f.created, event)
}

func (f *fakeEvents) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
}
