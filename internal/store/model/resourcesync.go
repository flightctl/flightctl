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

type ResourceSync struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[domain.ResourceSyncSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[domain.ResourceSyncStatus] `gorm:"type:jsonb"`
}

func (rs *ResourceSync) String() string {
	val, _ := json.Marshal(rs)
	return string(val)
}

func NewResourceSyncFromApiResource(resource *domain.ResourceSync) (*ResourceSync, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &ResourceSync{}, nil
	}

	status := domain.ResourceSyncStatus{Conditions: []domain.Condition{}}
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
	return &ResourceSync{
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

func ResourceSyncAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.ResourceSyncAPIVersion)
}

func (rs *ResourceSync) ToApiResource(opts ...APIResourceOption) (*domain.ResourceSync, error) {
	if rs == nil {
		return &domain.ResourceSync{}, nil
	}

	var spec domain.ResourceSyncSpec
	if rs.Spec != nil {
		spec = rs.Spec.Data
	}

	status := domain.ResourceSyncStatus{Conditions: []domain.Condition{}}
	if rs.Status != nil {
		status = rs.Status.Data
	}

	return &domain.ResourceSync{
		ApiVersion: ResourceSyncAPIVersion(),
		Kind:       domain.ResourceSyncKind,
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(rs.Name),
			CreationTimestamp: lo.ToPtr(rs.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(rs.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(rs.Resource.Annotations)),
			Generation:        rs.Generation,
			ResourceVersion:   lo.Ternary(rs.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(rs.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func ResourceSyncsToApiResource(rss []ResourceSync, cont *string, numRemaining *int64) (domain.ResourceSyncList, error) {
	resourceSyncList := make([]domain.ResourceSync, len(rss))
	for i, resourceSync := range rss {
		apiResource, _ := resourceSync.ToApiResource()
		resourceSyncList[i] = *apiResource
	}
	ret := domain.ResourceSyncList{
		ApiVersion: ResourceSyncAPIVersion(),
		Kind:       domain.ResourceSyncListKind,
		Items:      resourceSyncList,
		Metadata:   domain.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (rs *ResourceSync) GetKind() string {
	return domain.ResourceSyncKind
}

func (rs *ResourceSync) HasNilSpec() bool {
	return rs.Spec == nil
}

func (rs *ResourceSync) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*ResourceSync) // Assert that the other resource is a *ResourceSync
	if !ok {
		return false // Not the same type, so specs cannot be the same
	}
	if other == nil {
		return false
	}
	if rs.Spec == nil && other.Spec == nil {
		return true
	}
	if (rs.Spec == nil && other.Spec != nil) || (rs.Spec != nil && other.Spec == nil) {
		return false
	}
	return reflect.DeepEqual(rs.Spec.Data, other.Spec.Data)
}

func (rs *ResourceSync) GetStatusAsJson() ([]byte, error) {
	if rs.Status == nil {
		return []byte("null"), nil
	}
	return rs.Status.MarshalJSON()
}
