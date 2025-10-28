package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type AuthProvider struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.AuthProviderSpec] `gorm:"type:jsonb"`
}

func (a AuthProvider) String() string {
	val, _ := json.Marshal(a)
	return string(val)
}

func NewAuthProviderFromApiResource(resource *api.AuthProvider) (*AuthProvider, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &AuthProvider{}, nil
	}

	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}

	return &AuthProvider{
		Resource: Resource{
			Name:            lo.FromPtr(resource.Metadata.Name),
			Owner:           resource.Metadata.Owner,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			Generation:      resource.Metadata.Generation,
			ResourceVersion: resourceVersion,
		},
		Spec: MakeJSONField(resource.Spec),
	}, nil
}

func AuthProviderAPIVersion() string {
	return fmt.Sprintf("%s/%s", api.APIGroup, api.AuthProviderAPIVersion)
}

func (a *AuthProvider) ToApiResource(opts ...APIResourceOption) (*api.AuthProvider, error) {
	if a == nil {
		return &api.AuthProvider{}, nil
	}

	var spec api.AuthProviderSpec
	if a.Spec != nil {
		spec = a.Spec.Data
	}

	return &api.AuthProvider{
		ApiVersion: AuthProviderAPIVersion(),
		Kind:       api.AuthProviderKind,
		Metadata: api.ObjectMeta{
			Name:              lo.ToPtr(a.Name),
			CreationTimestamp: lo.ToPtr(a.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(a.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(a.Resource.Annotations)),
			Generation:        a.Generation,
			ResourceVersion:   lo.Ternary(a.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(a.ResourceVersion), 10)), nil),
		},
		Spec: spec,
	}, nil
}

func (a *AuthProvider) GetKind() string {
	return api.AuthProviderKind
}

func (a *AuthProvider) GetStatusAsJson() ([]byte, error) {
	return []byte("{}"), nil
}

func (a *AuthProvider) HasNilSpec() bool {
	return a.Spec == nil
}

func (a *AuthProvider) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*AuthProvider) // Assert that the other resource is a *AuthProvider
	if !ok {
		return false
	}
	return reflect.DeepEqual(a.Spec.Data, other.Spec.Data)
}

func AuthProvidersToApiResource(authProviders []AuthProvider, continueToken *string, resourceVersion *int64) (api.AuthProviderList, error) {
	items := lo.Map(authProviders, func(authProvider AuthProvider, _ int) api.AuthProvider {
		resource, _ := authProvider.ToApiResource()
		return *resource
	})

	return api.AuthProviderList{
		ApiVersion: AuthProviderAPIVersion(),
		Kind:       api.AuthProviderKind,
		Metadata: api.ListMeta{
			Continue: continueToken,
		},
		Items: items,
	}, nil
}
