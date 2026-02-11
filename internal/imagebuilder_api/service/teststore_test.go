package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/kvstore"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// DummyImageBuildStore is a mock implementation of store.ImageBuildStore
type DummyImageBuildStore struct {
	imageBuilds      *[]api.ImageBuild
	imageExportStore *DummyImageExportStore
}

func NewDummyImageBuildStore() *DummyImageBuildStore {
	return &DummyImageBuildStore{
		imageBuilds: &[]api.ImageBuild{},
	}
}

func NewDummyImageBuildStoreWithExports(imageExportStore *DummyImageExportStore) *DummyImageBuildStore {
	return &DummyImageBuildStore{
		imageBuilds:      &[]api.ImageBuild{},
		imageExportStore: imageExportStore,
	}
}

func (s *DummyImageBuildStore) Create(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error) {
	// Check for duplicate
	for _, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == lo.FromPtr(imageBuild.Metadata.Name) {
			return nil, flterrors.ErrDuplicateName
		}
	}

	var created api.ImageBuild
	deepCopy(imageBuild, &created)
	now := time.Now()
	created.Metadata.CreationTimestamp = &now
	*s.imageBuilds = append(*s.imageBuilds, created)
	return &created, nil
}

func (s *DummyImageBuildStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...store.GetOption) (*api.ImageBuild, error) {
	// Extract withExports value from options
	options := store.GetOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	withExports := options.WithExports

	for _, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == name {
			var result api.ImageBuild
			deepCopy(ib, &result)

			// If withExports is true, find related ImageExports
			if withExports && s.imageExportStore != nil {
				var imageExports []api.ImageExport
				for _, export := range *s.imageExportStore.imageExports {
					if buildRefSource, err := export.Spec.Source.AsImageBuildRefSource(); err == nil {
						if buildRefSource.ImageBuildRef == name {
							var exportCopy api.ImageExport
							deepCopy(export, &exportCopy)
							imageExports = append(imageExports, exportCopy)
						}
					}
				}
				if len(imageExports) > 0 {
					result.Imageexports = &imageExports
				}
			}

			return &result, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyImageBuildStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams, opts ...store.ListOption) (*api.ImageBuildList, error) {
	// Extract withExports value from options
	options := store.ListOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	withExports := options.WithExports

	items := make([]api.ImageBuild, len(*s.imageBuilds))
	for i, ib := range *s.imageBuilds {
		deepCopy(ib, &items[i])
	}

	// Apply limit if specified
	if listParams.Limit > 0 && len(items) > listParams.Limit {
		items = items[:listParams.Limit]
	}

	// If withExports is true, find related ImageExports for each ImageBuild
	if withExports && s.imageExportStore != nil {
		// Build a map of ImageBuild name to ImageExports
		exportsMap := make(map[string][]api.ImageExport)
		for _, export := range *s.imageExportStore.imageExports {
			if buildRefSource, err := export.Spec.Source.AsImageBuildRefSource(); err == nil {
				buildName := buildRefSource.ImageBuildRef
				var exportCopy api.ImageExport
				deepCopy(export, &exportCopy)
				exportsMap[buildName] = append(exportsMap[buildName], exportCopy)
			}
		}

		// Attach ImageExports to each ImageBuild
		for i := range items {
			buildName := lo.FromPtr(items[i].Metadata.Name)
			if exports, ok := exportsMap[buildName]; ok {
				items[i].Imageexports = &exports
			}
		}
	}

	return &api.ImageBuildList{
		ApiVersion: api.ImageBuildAPIVersion,
		Kind:       api.ImageBuildListKind,
		Items:      items,
	}, nil
}

func (s *DummyImageBuildStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, error) {
	for i, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == name {
			var deleted api.ImageBuild
			deepCopy(ib, &deleted)

			// Cascading delete: remove related ImageExports
			if s.imageExportStore != nil {
				s.imageExportStore.DeleteByImageBuildRef(name)
			}

			*s.imageBuilds = append((*s.imageBuilds)[:i], (*s.imageBuilds)[i+1:]...)
			return &deleted, nil
		}
	}
	// Idempotent delete - return (nil, nil) if resource doesn't exist
	return nil, nil
}

func (s *DummyImageBuildStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageBuild *api.ImageBuild) (*api.ImageBuild, error) {
	for i, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == lo.FromPtr(imageBuild.Metadata.Name) {
			(*s.imageBuilds)[i].Status = imageBuild.Status
			var result api.ImageBuild
			deepCopy((*s.imageBuilds)[i], &result)
			return &result, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyImageBuildStore) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	for i, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == name {
			if (*s.imageBuilds)[i].Status == nil {
				(*s.imageBuilds)[i].Status = &api.ImageBuildStatus{}
			}
			(*s.imageBuilds)[i].Status.LastSeen = &timestamp
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
}

func (s *DummyImageBuildStore) UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error {
	for _, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == name {
			// In a real implementation, this would update a Logs field
			// For the dummy store, we just return success
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
}

func (s *DummyImageBuildStore) GetLogs(ctx context.Context, orgId uuid.UUID, name string) (string, error) {
	for _, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == name {
			// In a real implementation, this would return the Logs field
			// For the dummy store, we just return empty string
			return "", nil
		}
	}
	return "", flterrors.ErrResourceNotFound
}

func (s *DummyImageBuildStore) InitialMigration(ctx context.Context) error {
	return nil
}

// DummyImageExportStore is a mock implementation of store.ImageExportStore
type DummyImageExportStore struct {
	imageExports *[]api.ImageExport
	nextRetryAt  map[string]*time.Time // tracks nextRetryAt by name since it's not in API struct
}

func NewDummyImageExportStore() *DummyImageExportStore {
	return &DummyImageExportStore{
		imageExports: &[]api.ImageExport{},
		nextRetryAt:  make(map[string]*time.Time),
	}
}

func (s *DummyImageExportStore) Create(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error) {
	// Check for duplicate
	for _, ie := range *s.imageExports {
		if lo.FromPtr(ie.Metadata.Name) == lo.FromPtr(imageExport.Metadata.Name) {
			return nil, flterrors.ErrDuplicateName
		}
	}

	var created api.ImageExport
	deepCopy(imageExport, &created)
	now := time.Now()
	created.Metadata.CreationTimestamp = &now
	*s.imageExports = append(*s.imageExports, created)
	return &created, nil
}

func (s *DummyImageExportStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, error) {
	for _, ie := range *s.imageExports {
		if lo.FromPtr(ie.Metadata.Name) == name {
			var result api.ImageExport
			deepCopy(ie, &result)
			return &result, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyImageExportStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*api.ImageExportList, error) {
	items := make([]api.ImageExport, len(*s.imageExports))
	for i, ie := range *s.imageExports {
		deepCopy(ie, &items[i])
	}

	// Apply limit if specified
	if listParams.Limit > 0 && len(items) > listParams.Limit {
		items = items[:listParams.Limit]
	}

	return &api.ImageExportList{
		ApiVersion: api.ImageExportAPIVersion,
		Kind:       api.ImageExportListKind,
		Items:      items,
	}, nil
}

func (s *DummyImageExportStore) Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, error) {
	for i, ie := range *s.imageExports {
		if lo.FromPtr(ie.Metadata.Name) == name {
			var deleted api.ImageExport
			deepCopy(ie, &deleted)
			*s.imageExports = append((*s.imageExports)[:i], (*s.imageExports)[i+1:]...)
			return &deleted, nil
		}
	}
	// Idempotent delete - return (nil, nil) if resource doesn't exist
	return nil, nil
}

// DeleteByImageBuildRef deletes all ImageExports that reference the given ImageBuild name.
// This is used for cascading delete when an ImageBuild is deleted.
func (s *DummyImageExportStore) DeleteByImageBuildRef(imageBuildName string) {
	filtered := make([]api.ImageExport, 0, len(*s.imageExports))
	for _, ie := range *s.imageExports {
		source, err := ie.Spec.Source.AsImageBuildRefSource()
		if err != nil || source.ImageBuildRef != imageBuildName {
			filtered = append(filtered, ie)
		}
	}
	*s.imageExports = filtered
}

func (s *DummyImageExportStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error) {
	for i, ie := range *s.imageExports {
		if lo.FromPtr(ie.Metadata.Name) == lo.FromPtr(imageExport.Metadata.Name) {
			(*s.imageExports)[i].Status = imageExport.Status
			var result api.ImageExport
			deepCopy((*s.imageExports)[i], &result)
			return &result, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyImageExportStore) UpdateNextRetryAt(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	for _, ie := range *s.imageExports {
		if lo.FromPtr(ie.Metadata.Name) == name {
			s.nextRetryAt[name] = &timestamp
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
}

func (s *DummyImageExportStore) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	for i, ie := range *s.imageExports {
		if lo.FromPtr(ie.Metadata.Name) == name {
			if (*s.imageExports)[i].Status == nil {
				(*s.imageExports)[i].Status = &api.ImageExportStatus{}
			}
			(*s.imageExports)[i].Status.LastSeen = &timestamp
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
}

func (s *DummyImageExportStore) ListPendingRetry(ctx context.Context, orgId uuid.UUID, beforeTime time.Time) (*api.ImageExportList, error) {
	var items []api.ImageExport
	for _, ie := range *s.imageExports {
		name := lo.FromPtr(ie.Metadata.Name)
		nextRetry := s.nextRetryAt[name]
		if nextRetry != nil && nextRetry.Before(beforeTime) {
			// Check condition reason to determine if terminal
			isTerminal := false
			if ie.Status != nil && ie.Status.Conditions != nil {
				for _, cond := range *ie.Status.Conditions {
					if cond.Type == api.ImageExportConditionTypeReady {
						if cond.Reason == string(api.ImageExportConditionReasonCompleted) ||
							cond.Reason == string(api.ImageExportConditionReasonFailed) {
							isTerminal = true
						}
						break
					}
				}
			}
			if !isTerminal {
				var item api.ImageExport
				deepCopy(ie, &item)
				items = append(items, item)
			}
		}
	}
	return &api.ImageExportList{
		ApiVersion: api.ImageExportAPIVersion,
		Kind:       api.ImageExportListKind,
		Items:      items,
	}, nil
}

func (s *DummyImageExportStore) InitialMigration(ctx context.Context) error {
	return nil
}

func (s *DummyImageExportStore) UpdateLogs(ctx context.Context, orgId uuid.UUID, name string, logs string) error {
	return nil
}

func (s *DummyImageExportStore) GetLogs(ctx context.Context, orgId uuid.UUID, name string) (string, error) {
	return "", nil
}

// DummyRepositoryStore is a mock implementation of flightctlstore.Repository
type DummyRepositoryStore struct {
	repositories map[string]*domain.Repository // key: name
}

func NewDummyRepositoryStore() *DummyRepositoryStore {
	return &DummyRepositoryStore{
		repositories: make(map[string]*domain.Repository),
	}
}

func (s *DummyRepositoryStore) InitialMigration(ctx context.Context) error {
	return nil
}

func (s *DummyRepositoryStore) Create(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback flightctlstore.EventCallback) (*domain.Repository, error) {
	name := lo.FromPtr(repository.Metadata.Name)
	if _, exists := s.repositories[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	var created domain.Repository
	deepCopy(repository, &created)
	s.repositories[name] = &created
	return &created, nil
}

func (s *DummyRepositoryStore) Update(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback flightctlstore.EventCallback) (*domain.Repository, error) {
	name := lo.FromPtr(repository.Metadata.Name)
	if _, exists := s.repositories[name]; !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	var updated domain.Repository
	deepCopy(repository, &updated)
	s.repositories[name] = &updated
	return &updated, nil
}

func (s *DummyRepositoryStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback flightctlstore.EventCallback) (*domain.Repository, bool, error) {
	name := lo.FromPtr(repository.Metadata.Name)
	created := false
	if _, exists := s.repositories[name]; !exists {
		created = true
	}
	var result domain.Repository
	deepCopy(repository, &result)
	s.repositories[name] = &result
	return &result, created, nil
}

func (s *DummyRepositoryStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, error) {
	repo, exists := s.repositories[name]
	if !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	var result domain.Repository
	deepCopy(repo, &result)
	return &result, nil
}

func (s *DummyRepositoryStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*domain.RepositoryList, error) {
	return &domain.RepositoryList{}, nil
}

func (s *DummyRepositoryStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback flightctlstore.EventCallback) error {
	return nil
}

func (s *DummyRepositoryStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.Repository, eventCallback flightctlstore.EventCallback) (*domain.Repository, error) {
	return nil, nil
}

func (s *DummyRepositoryStore) GetFleetRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.FleetList, error) {
	return &domain.FleetList{}, nil
}

func (s *DummyRepositoryStore) GetDeviceRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceList, error) {
	return &domain.DeviceList{}, nil
}

func (s *DummyRepositoryStore) Count(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (int64, error) {
	return 0, nil
}

func (s *DummyRepositoryStore) CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]flightctlstore.CountByOrgResult, error) {
	return nil, nil
}

// newOciRepository creates a test OCI repository with the specified access mode
func newOciRepository(name string, accessMode v1beta1.OciRepoSpecAccessMode) *v1beta1.Repository {
	spec := v1beta1.RepositorySpec{}
	_ = spec.FromOciRepoSpec(v1beta1.OciRepoSpec{
		Registry:   "quay.io",
		Type:       v1beta1.OciRepoSpecTypeOci,
		AccessMode: &accessMode,
	})
	return &v1beta1.Repository{
		ApiVersion: "flightctl.io/v1beta1",
		Kind:       string(v1beta1.ResourceKindRepository),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}
}

// deepCopy performs a deep copy using JSON marshaling
func deepCopy(src, dst interface{}) {
	data, err := json.Marshal(src)
	if err != nil {
		panic(fmt.Sprintf("deepCopy failed in test: %v", err))
	}
	if err = json.Unmarshal(data, dst); err != nil {
		panic(fmt.Sprintf("deepCopy failed in test: %v", err))
	}
}

// DummyStore is a mock implementation of store.Store for unit testing
type DummyStore struct {
	imageBuildStore  *DummyImageBuildStore
	imageExportStore *DummyImageExportStore
}

func NewDummyStore() *DummyStore {
	return &DummyStore{
		imageBuildStore:  NewDummyImageBuildStore(),
		imageExportStore: NewDummyImageExportStore(),
	}
}

func (s *DummyStore) ImageBuild() store.ImageBuildStore {
	return s.imageBuildStore
}

func (s *DummyStore) ImageExport() store.ImageExportStore {
	return s.imageExportStore
}

func (s *DummyStore) RunMigrations(ctx context.Context) error {
	return nil
}

func (s *DummyStore) Ping() error {
	return nil
}

func (s *DummyStore) Close() error {
	return nil
}

// DummyKVStore is a mock implementation of kvstore.KVStore for unit testing
type DummyKVStore struct {
	streams map[string][][]byte
	mu      sync.Mutex
	// canceledSignals maps stream keys to whether a "canceled" signal should be returned
	// When a key is present and true, StreamRead will return the signal once
	canceledSignals map[string]bool
}

func NewDummyKVStore() *DummyKVStore {
	return &DummyKVStore{
		streams:         make(map[string][][]byte),
		canceledSignals: make(map[string]bool),
	}
}

// SimulateCanceledSignal configures the mock to return a "canceled" signal when
// StreamRead is called for the given key
func (s *DummyKVStore) SimulateCanceledSignal(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceledSignals[key] = true
}

func (s *DummyKVStore) StreamAdd(ctx context.Context, key string, value []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams[key] == nil {
		s.streams[key] = make([][]byte, 0)
	}
	s.streams[key] = append(s.streams[key], value)
	return "0-0", nil
}

func (s *DummyKVStore) SetExpire(ctx context.Context, key string, expiration time.Duration) error {
	return nil
}

func (s *DummyKVStore) StreamRange(ctx context.Context, key string, start, stop string) ([]kvstore.StreamEntry, error) {
	return nil, nil
}

func (s *DummyKVStore) StreamRead(ctx context.Context, key string, lastID string, block time.Duration, count int64) ([]kvstore.StreamEntry, error) {
	s.mu.Lock()
	// Check if we should return a canceled signal for this key
	if s.canceledSignals[key] {
		// Return the signal once, then mark as consumed
		delete(s.canceledSignals, key)
		s.mu.Unlock()
		return []kvstore.StreamEntry{
			{ID: "0-1", Value: []byte("canceled")},
		}, nil
	}
	s.mu.Unlock()

	// Simulate Redis XREAD BLOCK - wait for the block duration or context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(block):
		// Timeout - return empty (no signal)
		return nil, nil
	}
}

func (s *DummyKVStore) SetNX(ctx context.Context, key string, value []byte) (bool, error) {
	return false, nil
}

func (s *DummyKVStore) SetIfGreater(ctx context.Context, key string, newVal int64) (bool, error) {
	return false, nil
}

func (s *DummyKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	return nil, nil
}

func (s *DummyKVStore) GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error) {
	return nil, nil
}

func (s *DummyKVStore) DeleteKeysForTemplateVersion(ctx context.Context, key string) error {
	return nil
}

func (s *DummyKVStore) DeleteAllKeys(ctx context.Context) error {
	return nil
}

func (s *DummyKVStore) PrintAllKeys(ctx context.Context) {}

func (s *DummyKVStore) Delete(ctx context.Context, key string) error {
	return nil
}

func (s *DummyKVStore) Close() {}
