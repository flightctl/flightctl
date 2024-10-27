package model

import (
	"encoding/json"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

var (
	RepositoryAPI      = "v1alpha1"
	RepositoryKind     = "Repository"
	RepositoryListKind = "RepositoryList"
)

type Repository struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.RepositorySpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.RepositoryStatus] `gorm:"type:jsonb" selector:"status"`

	// Join table with the relationship of repository to fleets
	Fleets []Fleet `gorm:"many2many:fleet_repos;constraint:OnDelete:CASCADE;"`

	// Join table with the relationship of repository to devices (only maintained for standalone devices)
	Devices []Device `gorm:"many2many:device_repos;constraint:OnDelete:CASCADE;"`
}

type RepositoryList []Repository

func (d Repository) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewRepositoryFromApiResource(resource *api.Repository) (*Repository, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &Repository{}, nil
	}

	status := api.RepositoryStatus{Conditions: []api.Condition{}}
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
			Labels:          util.LabelMapToArray(resource.Metadata.Labels),
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func hideValue(value *string) {
	if value != nil {
		*value = *util.StrToPtr("*****")
	}
}

func (f *Repository) ToApiResource() (api.Repository, error) {
	if f == nil {
		return api.Repository{}, nil
	}

	var spec api.RepositorySpec
	if f.Spec != nil {
		spec = f.Spec.Data
	}

	status := api.RepositoryStatus{Conditions: []api.Condition{}}
	if f.Status != nil {
		status = f.Status.Data
	}

	_, err := spec.GetGenericRepoSpec()
	if err != nil {
		gitHttpSpec, err := spec.GetHttpRepoSpec()
		if err == nil {
			hideValue(gitHttpSpec.HttpConfig.Password)
			hideValue(gitHttpSpec.HttpConfig.TlsKey)
			hideValue(gitHttpSpec.HttpConfig.TlsCrt)
			if err := spec.FromHttpRepoSpec(gitHttpSpec); err != nil {
				return api.Repository{}, err
			}

		} else {
			gitSshRepoSpec, err := spec.GetSshRepoSpec()
			if err == nil {
				hideValue(gitSshRepoSpec.SshConfig.SshPrivateKey)
				hideValue(gitSshRepoSpec.SshConfig.PrivateKeyPassphrase)
				if err := spec.FromSshRepoSpec(gitSshRepoSpec); err != nil {
					return api.Repository{}, err
				}
			}
		}
	}
	metadataLabels := util.LabelArrayToMap(f.Resource.Labels)

	return api.Repository{
		ApiVersion: RepositoryAPI,
		Kind:       RepositoryKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(f.Name),
			CreationTimestamp: util.TimeToPtr(f.CreatedAt.UTC()),
			Labels:            &metadataLabels,
			ResourceVersion:   lo.Ternary(f.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(f.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func (dl RepositoryList) ToApiResource(cont *string, numRemaining *int64) (api.RepositoryList, error) {
	if dl == nil {
		return api.RepositoryList{
			ApiVersion: RepositoryAPI,
			Kind:       RepositoryListKind,
			Items:      []api.Repository{},
		}, nil
	}

	repositoryList := make([]api.Repository, len(dl))
	for i, repository := range dl {
		repo, err := repository.ToApiResource()
		if err != nil {
			return api.RepositoryList{
				ApiVersion: RepositoryAPI,
				Kind:       RepositoryListKind,
				Items:      []api.Repository{},
			}, err
		}
		repositoryList[i] = repo
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
	return ret, nil
}
