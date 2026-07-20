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
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
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
	// Status.LastSeen is tagged `json:"-"` (the real store persists it as its own DB
	// column, not as part of the JSON status blob), so it doesn't survive the JSON
	// round trip above; copy it explicitly to mirror real persistence.
	if src.Status != nil && dst.Status != nil {
		dst.Status.LastSeen = src.Status.LastSeen
	}
	return dst
}

// fakeStore is a plain test-only container grouping the fake deviceStore/fleetStore this
// package's DeviceServiceHandler now takes as two separate narrow constructor params. It
// implements no store interface itself - just a convenience holder so handler_test.go's many
// call sites can keep referencing st.device/st.fleet unchanged.
type fakeStore struct {
	device *fakeDeviceStore
	fleet  *fakeFleetStore
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		device: &fakeDeviceStore{devices: map[string]*domain.Device{}, repoRefs: map[string][]string{}},
		fleet:  &fakeFleetStore{fleets: map[string]*domain.Fleet{}},
	}
}

// fakeDeviceStore is a minimal in-memory stand-in for devicestore.Store, implementing only the
// methods this package's handler_test.go exercises.
type fakeDeviceStore struct {
	devicestore.Store
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

func (s *fakeDeviceStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, validationCallback devicestore.DeviceStoreValidationCallback, eventCallback store.EventCallback) (*domain.Device, bool, error) {
	name := lo.FromPtr(device.Metadata.Name)
	old, existed := s.devices[name]
	if existed && validationCallback != nil {
		if err := validationCallback(ctx, old, device); err != nil {
			return nil, false, err
		}
	}
	// Mirrors the real generic store: fields left nil by the caller are preserved
	// from the existing resource rather than wiped on update.
	if existed && device.Metadata.Owner == nil {
		device.Metadata.Owner = old.Metadata.Owner
	}
	d := deepCopyDevice(device)
	s.devices[name] = d
	created := !existed
	if eventCallback != nil {
		eventCallback(ctx, domain.DeviceKind, orgId, name, old, d, created, nil)
	}
	return deepCopyDevice(d), created, nil
}

func (s *fakeDeviceStore) Update(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, validationCallback devicestore.DeviceStoreValidationCallback, eventCallback store.EventCallback) (*domain.Device, error) {
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
	// Mirrors the real generic store: fields left nil by the caller are preserved
	// from the existing resource rather than wiped on update.
	if device.Metadata.Owner == nil {
		device.Metadata.Owner = old.Metadata.Owner
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

func (s *fakeDeviceStore) List(ctx context.Context, orgId uuid.UUID, listParams devicestore.DeviceListParams) (*domain.DeviceList, error) {
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

func (s *fakeDeviceStore) MutateAnnotation(ctx context.Context, orgId uuid.UUID, name string, key string, mutate func(current string) (string, error)) error {
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
	newValue, err := mutate(current[key])
	if err != nil {
		return err
	}
	current[key] = newValue
	d.Metadata.Annotations = &current
	return nil
}

func (s *fakeDeviceStore) UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string, configFingerprints []domain.DependencySyncConfigRefStatus, forceUpdate bool) (string, error) {
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

func (s *fakeDeviceStore) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, callback devicestore.ServiceConditionsCallback) error {
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

// fakeFleetStore is a minimal stand-in for fleetstore.Store, implementing only Get - the single
// call site common.UpdateServiceSideStatus reaches for managed-device status computation.
type fakeFleetStore struct {
	fleetstore.Store
	fleets   map[string]*domain.Fleet
	getCalls int
}

func (s *fakeFleetStore) Get(ctx context.Context, orgId uuid.UUID, name string, options ...fleetstore.GetOption) (*domain.Fleet, error) {
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
