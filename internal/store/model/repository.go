package model

import (
	"encoding/json"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
)

var (
	RepositoryAPI      = "v1alpha1"
	RepositoryKind     = "Repository"
	RepositoryListKind = "RepositoryList"
)

type Repository struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.RepositorySpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.RepositoryStatus]
}

type RepositoryList []Repository

func (d Repository) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewRepositoryFromApiResource(resource *api.Repository) *Repository {
	if resource == nil || resource.Metadata.Name == nil {
		return &Repository{}
	}

	var status api.RepositoryStatus
	if resource.Status != nil {
		status = *resource.Status
	}
	return &Repository{
		Resource: Resource{
			Name:   *resource.Metadata.Name,
			Labels: util.LabelMapToArray(resource.Metadata.Labels),
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}
}

func (f *Repository) ToApiResource() api.Repository {
	if f == nil {
		return api.Repository{}
	}

	var spec api.RepositorySpec
	if f.Spec != nil {
		spec = f.Spec.Data
	}

	var status api.RepositoryStatus
	if f.Status != nil {
		status = f.Status.Data
	}

	if spec.Password != nil {
		spec.Password = util.StrToPtr("*****")
	}

	metadataLabels := util.LabelArrayToMap(f.Resource.Labels)

	return api.Repository{
		ApiVersion: RepositoryAPI,
		Kind:       RepositoryKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(f.Name),
			CreationTimestamp: util.StrToPtr(f.CreatedAt.UTC().Format(time.RFC3339)),
			Labels:            &metadataLabels,
		},
		Spec:   spec,
		Status: &status,
	}
}

func (dl RepositoryList) ToApiResource(cont *string, numRemaining *int64) api.RepositoryList {
	if dl == nil {
		return api.RepositoryList{
			ApiVersion: RepositoryAPI,
			Kind:       RepositoryListKind,
			Items:      []api.Repository{},
		}
	}

	repositoryList := make([]api.Repository, len(dl))
	for i, repository := range dl {
		repositoryList[i] = repository.ToApiResource()
	}
	ret := api.RepositoryList{
		ApiVersion: RepositoryAPI,
		Kind:       RepositoryListKind,
		Items:      repositoryList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret
}
