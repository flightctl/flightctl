package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// DummyImageBuildStore is a mock implementation of store.ImageBuildStore
type DummyImageBuildStore struct {
	imageBuilds *[]api.ImageBuild
}

func NewDummyImageBuildStore() *DummyImageBuildStore {
	return &DummyImageBuildStore{
		imageBuilds: &[]api.ImageBuild{},
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

func (s *DummyImageBuildStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageBuild, error) {
	for _, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == name {
			var result api.ImageBuild
			deepCopy(ib, &result)
			return &result, nil
		}
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyImageBuildStore) List(ctx context.Context, orgId uuid.UUID, listParams flightctlstore.ListParams) (*api.ImageBuildList, error) {
	items := make([]api.ImageBuild, len(*s.imageBuilds))
	for i, ib := range *s.imageBuilds {
		deepCopy(ib, &items[i])
	}

	// Apply limit if specified
	if listParams.Limit > 0 && len(items) > listParams.Limit {
		items = items[:listParams.Limit]
	}

	return &api.ImageBuildList{
		ApiVersion: api.ImageBuildAPIVersion,
		Kind:       api.ImageBuildListKind,
		Items:      items,
	}, nil
}

func (s *DummyImageBuildStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	for i, ib := range *s.imageBuilds {
		if lo.FromPtr(ib.Metadata.Name) == name {
			*s.imageBuilds = append((*s.imageBuilds)[:i], (*s.imageBuilds)[i+1:]...)
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
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

func (s *DummyImageExportStore) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	for i, ie := range *s.imageExports {
		if lo.FromPtr(ie.Metadata.Name) == name {
			*s.imageExports = append((*s.imageExports)[:i], (*s.imageExports)[i+1:]...)
			return nil
		}
	}
	return flterrors.ErrResourceNotFound
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

// DummyImagePipelineStore is a mock implementation of store.ImagePipelineStore
type DummyImagePipelineStore struct{}

func NewDummyImagePipelineStore() *DummyImagePipelineStore {
	return &DummyImagePipelineStore{}
}

// Transaction executes fn within a simulated transaction for unit tests
// For the dummy store, this just executes the callback immediately
func (s *DummyImagePipelineStore) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// DummyStore is a mock implementation of store.Store for unit testing
type DummyStore struct {
	imageBuildStore    *DummyImageBuildStore
	imageExportStore   *DummyImageExportStore
	imagePipelineStore *DummyImagePipelineStore
}

func NewDummyStore() *DummyStore {
	return &DummyStore{
		imageBuildStore:    NewDummyImageBuildStore(),
		imageExportStore:   NewDummyImageExportStore(),
		imagePipelineStore: NewDummyImagePipelineStore(),
	}
}

func (s *DummyStore) ImageBuild() store.ImageBuildStore {
	return s.imageBuildStore
}

func (s *DummyStore) ImageExport() store.ImageExportStore {
	return s.imageExportStore
}

func (s *DummyStore) ImagePipeline() store.ImagePipelineStore {
	return s.imagePipelineStore
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
