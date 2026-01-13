package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type Repository struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[domain.RepositorySpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[domain.RepositoryStatus] `gorm:"type:jsonb"`

	// Join table with the relationship of repository to fleets
	Fleets []Fleet `gorm:"many2many:fleet_repos;constraint:OnDelete:CASCADE;"`

	// Join table with the relationship of repository to devices (only maintained for standalone devices)
	Devices []Device `gorm:"many2many:device_repos;constraint:OnDelete:CASCADE;"`
}

func (r Repository) String() string {
	val, _ := json.Marshal(r)
	return string(val)
}

func NewRepositoryFromApiResource(resource *domain.Repository) (*Repository, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &Repository{}, nil
	}

	status := domain.RepositoryStatus{Conditions: []domain.Condition{}}
	if resource.Status != nil {
		status = *resource.Status
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}
	return &Repository{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func RepositoryAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.RepositoryAPIVersion)
}

func (r *Repository) ToApiResource(opts ...APIResourceOption) (*domain.Repository, error) {
	if r == nil {
		return &domain.Repository{}, nil
	}

	var spec domain.RepositorySpec
	if r.Spec != nil {
		spec = r.Spec.Data
	}

	status := domain.RepositoryStatus{Conditions: []domain.Condition{}}
	if r.Status != nil {
		status = r.Status.Data
	}

	return &domain.Repository{
		ApiVersion: RepositoryAPIVersion(),
		Kind:       domain.RepositoryKind,
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(r.Name),
			CreationTimestamp: lo.ToPtr(r.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(r.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(r.Resource.Annotations)),
			ResourceVersion:   lo.Ternary(r.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(r.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func RepositoriesToApiResource(repos []Repository, cont *string, numRemaining *int64) (domain.RepositoryList, error) {
	repositoryList := make([]domain.Repository, len(repos))
	for i, repository := range repos {
		repo, err := repository.ToApiResource()
		if err != nil {
			return domain.RepositoryList{
				ApiVersion: RepositoryAPIVersion(),
				Kind:       domain.RepositoryListKind,
				Items:      []domain.Repository{},
			}, err
		}
		repositoryList[i] = *repo
	}
	ret := domain.RepositoryList{
		ApiVersion: RepositoryAPIVersion(),
		Kind:       domain.RepositoryListKind,
		Items:      repositoryList,
		Metadata:   domain.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (r *Repository) GetKind() string {
	return domain.RepositoryKind
}

func (r *Repository) HasNilSpec() bool {
	return r.Spec == nil
}

func (r *Repository) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*Repository) // Assert that the other resource is a *Repository
	if !ok {
		return false // Not the same type, so specs cannot be the same
	}
	if other == nil {
		return false
	}
	if r.Spec == nil && other.Spec == nil {
		return true
	}
	if (r.Spec == nil && other.Spec != nil) || (r.Spec != nil && other.Spec == nil) {
		return false
	}
	return reflect.DeepEqual(r.Spec.Data, other.Spec.Data)
}

func (r *Repository) GetStatusAsJson() ([]byte, error) {
	return r.Status.MarshalJSON()
}
