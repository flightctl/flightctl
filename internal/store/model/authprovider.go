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

type AuthProvider struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[domain.AuthProviderSpec] `gorm:"type:jsonb"`
}

func (a AuthProvider) String() string {
	val, _ := json.Marshal(a)
	return string(val)
}

func NewAuthProviderFromApiResource(resource *domain.AuthProvider) (*AuthProvider, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is nil")
	}
	if resource.Metadata.Name == nil {
		return nil, fmt.Errorf("resource.Metadata.Name is nil")
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
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.AuthProviderAPIVersion)
}

func (a *AuthProvider) ToApiResource(opts ...APIResourceOption) (*domain.AuthProvider, error) {
	if a == nil {
		return &domain.AuthProvider{}, nil
	}

	var spec domain.AuthProviderSpec
	if a.Spec != nil {
		spec = a.Spec.Data
	}

	return &domain.AuthProvider{
		ApiVersion: AuthProviderAPIVersion(),
		Kind:       domain.AuthProviderKind,
		Metadata: domain.ObjectMeta{
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
	return domain.AuthProviderKind
}

func (a *AuthProvider) GetStatusAsJson() ([]byte, error) {
	return []byte("{}"), nil
}

func (a *AuthProvider) HasNilSpec() bool {
	return a.Spec == nil
}

func (a *AuthProvider) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*AuthProvider)
	if !ok {
		return false
	}
	if a == nil || other == nil {
		return false
	}
	if a.Spec == nil && other.Spec == nil {
		return true
	}
	if a.Spec == nil || other.Spec == nil {
		return false
	}
	return reflect.DeepEqual(a.Spec.Data, other.Spec.Data)
}

func AuthProvidersToApiResource(authProviders []AuthProvider, continueToken *string, resourceVersion *int64) (domain.AuthProviderList, error) {
	items := lo.Map(authProviders, func(authProvider AuthProvider, _ int) domain.AuthProvider {
		resource, _ := authProvider.ToApiResource()
		return *resource
	})

	return domain.AuthProviderList{
		ApiVersion: AuthProviderAPIVersion(),
		Kind:       domain.AuthProviderListKind,
		Metadata: domain.ListMeta{
			Continue: continueToken,
		},
		Items: items,
	}, nil
}
