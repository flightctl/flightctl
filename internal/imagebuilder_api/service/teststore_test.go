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

// TestStore is a mock implementation of store.Store for testing
type TestStore struct {
	imageBuild *DummyImageBuildStore
}

func NewTestStore() *TestStore {
	return &TestStore{
		imageBuild: &DummyImageBuildStore{
			imageBuilds: &[]api.ImageBuild{},
		},
	}
}

func (s *TestStore) ImageBuild() store.ImageBuildStore {
	return s.imageBuild
}

func (s *TestStore) RunMigrations(ctx context.Context) error {
	return nil
}

func (s *TestStore) Ping() error {
	return nil
}

func (s *TestStore) Close() error {
	return nil
}

// DummyImageBuildStore is a mock implementation of store.ImageBuildStore
type DummyImageBuildStore struct {
	imageBuilds *[]api.ImageBuild
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
