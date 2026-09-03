package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
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
	device  *fakeDeviceStore
	catalog *fakeCatalogStore
	fleet   *fakeFleetStore
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		device: &fakeDeviceStore{
			devices:  map[string]*domain.Device{},
			rendered: map[string]*devicestore.DeviceRendered{},
			repoRefs: map[string][]string{},
			lastSeen: map[string]*time.Time{},
		},
		catalog: &fakeCatalogStore{items: map[string]*domain.CatalogItem{}},
		fleet:   &fakeFleetStore{fleets: map[string]*domain.Fleet{}},
	}
}

// fakeDeviceStore is a minimal in-memory stand-in for devicestore.Store, implementing only the
// methods this package's handler_test.go exercises.
type fakeDeviceStore struct {
	devicestore.Store
	devices  map[string]*domain.Device
	rendered map[string]*devicestore.DeviceRendered
	repoRefs map[string][]string
	lastSeen map[string]*time.Time
}

func (s *fakeDeviceStore) rememberLastSeen(name string, device *domain.Device) {
	if s.lastSeen == nil {
		s.lastSeen = map[string]*time.Time{}
	}
	if device != nil && device.Status != nil && device.Status.LastSeen != nil {
		ls := *device.Status.LastSeen
		s.lastSeen[name] = &ls
	}
}

func (s *fakeDeviceStore) attachLastSeen(name string, device *domain.Device) {
	if device == nil || device.Status == nil {
		return
	}
	if ls, ok := s.lastSeen[name]; ok && ls != nil {
		cp := *ls
		device.Status.LastSeen = &cp
	}
}

func (s *fakeDeviceStore) Create(ctx context.Context, orgId uuid.UUID, device *domain.Device, rendered *devicestore.DeviceRendered) (*domain.Device, error) {
	name := lo.FromPtr(device.Metadata.Name)
	if _, exists := s.devices[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	d := deepCopyDevice(device)
	s.devices[name] = d
	s.rememberLastSeen(name, d)
	if rendered != nil {
		if s.rendered == nil {
			s.rendered = map[string]*devicestore.DeviceRendered{}
		}
		cp := *rendered
		s.rendered[name] = &cp
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
	d, err := s.Get(ctx, orgId, name)
	if err != nil {
		return nil, err
	}
	s.attachLastSeen(name, d)
	return d, nil
}

func (s *fakeDeviceStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.Device, apply devicestore.DeviceApplyFunc, opts ...devicestore.MutateOption) (*domain.Device, *domain.Device, bool, error) {
	old, ok := s.devices[name]
	creating := !ok
	var before *domain.Device
	var current *domain.Device
	if !creating {
		before = deepCopyDevice(old)
		current = deepCopyDevice(old)
		if devicestore.HasWithTimestamp(opts...) {
			s.attachLastSeen(name, before)
			s.attachLastSeen(name, current)
		}
	}
	mutation := &devicestore.DeviceMutation{Device: current}
	if apply != nil {
		if err := apply(mutation); err != nil {
			if errors.Is(err, store.ErrMutateSkipWrite) {
				if mutation.Device == nil {
					return nil, before, false, nil
				}
				return deepCopyDevice(mutation.Device), before, false, nil
			}
			return nil, nil, false, err
		}
	}
	if mutation.Device == nil {
		return nil, nil, false, flterrors.ErrResourceIsNil
	}
	d := deepCopyDevice(mutation.Device)
	s.devices[name] = d
	s.rememberLastSeen(name, d)
	if mutation.Rendered != nil {
		if s.rendered == nil {
			s.rendered = map[string]*devicestore.DeviceRendered{}
		}
		cp := *mutation.Rendered
		s.rendered[name] = &cp
	}
	return deepCopyDevice(d), before, creating, nil
}

func (s *fakeDeviceStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *domain.Device, previous *domain.Device) (*domain.Device, *domain.Device, error) {
	name := lo.FromPtr(device.Metadata.Name)
	updated, before, _, err := s.Mutate(ctx, orgId, name, previous, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		m.Device.Status = device.Status
		return nil
	})
	if err == nil {
		s.rememberLastSeen(name, device)
	}
	return updated, before, err
}

func (s *fakeDeviceStore) ReplaceServiceOwnedStatus(ctx context.Context, orgId uuid.UUID, device *domain.Device) (*domain.Device, *domain.Device, error) {
	return s.UpdateStatus(ctx, orgId, device, nil)
}

func (s *fakeDeviceStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	_, _, _, err := s.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		merged := store.MergeAnnotations(m.Device.Metadata.Annotations, annotations, deleteKeys)
		m.Device.Metadata.Annotations = &merged
		return nil
	})
	return err
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

func (s *fakeDeviceStore) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*domain.Device, error) {
	return s.Get(ctx, orgId, name)
}

func (s *fakeDeviceStore) GetLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*time.Time, error) {
	if _, ok := s.devices[name]; !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	if ls, ok := s.lastSeen[name]; ok && ls != nil {
		cp := *ls
		return &cp, nil
	}
	return nil, nil
}

func (s *fakeDeviceStore) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
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

// fakeCatalogStore is a minimal stand-in for catalogstore.Store, implementing only GetItem.
type fakeCatalogStore struct {
	catalogstore.Store
	items map[string]*domain.CatalogItem // key: "catalog/item"
}

func (s *fakeCatalogStore) GetItem(_ context.Context, _ uuid.UUID, catalogName string, itemName string) (*domain.CatalogItem, error) {
	key := catalogName + "/" + itemName
	item, ok := s.items[key]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return item, nil
}
