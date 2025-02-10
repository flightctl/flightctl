package model

import (
	"encoding/json"
	"reflect"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type ResourceSync struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.ResourceSyncSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.ResourceSyncStatus] `gorm:"type:jsonb"`
}

func (rs *ResourceSync) String() string {
	val, _ := json.Marshal(rs)
	return string(val)
}

func NewResourceSyncFromApiResource(resource *api.ResourceSync) (*ResourceSync, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &ResourceSync{}, nil
	}

	status := api.ResourceSyncStatus{Conditions: []api.Condition{}}
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

func (rs *ResourceSync) ToApiResource(opts ...APIResourceOption) (*api.ResourceSync, error) {
	if rs == nil {
		return &api.ResourceSync{}, nil
	}

	var spec api.ResourceSyncSpec
	if rs.Spec != nil {
		spec = rs.Spec.Data
	}

	status := api.ResourceSyncStatus{Conditions: []api.Condition{}}
	if rs.Status != nil {
		status = rs.Status.Data
	}

	return &api.ResourceSync{
		ApiVersion: api.ResourceSyncAPIVersion,
		Kind:       api.ResourceSyncKind,
		Metadata: api.ObjectMeta{
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

func ResourceSyncsToApiResource(rss []ResourceSync, cont *string, numRemaining *int64) (api.ResourceSyncList, error) {
	resourceSyncList := make([]api.ResourceSync, len(rss))
	for i, resourceSync := range rss {
		apiResource, _ := resourceSync.ToApiResource()
		resourceSyncList[i] = *apiResource
	}
	ret := api.ResourceSyncList{
		ApiVersion: api.ResourceSyncAPIVersion,
		Kind:       api.ResourceSyncListKind,
		Items:      resourceSyncList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

// NeedsSyncToHash returns true if the resource needs to be synced to the given hash.
func (rs *ResourceSync) NeedsSyncToHash(hash string) bool {
	if rs.Status == nil || rs.Status.Data.Conditions == nil {
		return true
	}

	if api.IsStatusConditionFalse(rs.Status.Data.Conditions, api.ResourceSyncSynced) {
		return true
	}

	var observedGen int64 = 0
	if rs.Status.Data.ObservedGeneration != nil {
		observedGen = *rs.Status.Data.ObservedGeneration
	}
	var prevHash string = util.DefaultIfNil(rs.Status.Data.ObservedCommit, "")
	return hash != prevHash || observedGen != *rs.Generation
}

func (rs *ResourceSync) ensureConditionsNotNil() {
	if rs.Status == nil {
		rs.Status = &JSONField[api.ResourceSyncStatus]{
			Data: api.ResourceSyncStatus{
				Conditions: []api.Condition{},
			},
		}
	}
	if rs.Status.Data.Conditions == nil {
		rs.Status.Data.Conditions = []api.Condition{}
	}
}

func (rs *ResourceSync) SetCondition(conditionType api.ConditionType, okReason, failReason string, err error) bool {
	rs.ensureConditionsNotNil()
	return api.SetStatusConditionByError(&rs.Status.Data.Conditions, conditionType, okReason, failReason, err)
}

func (rs *ResourceSync) AddRepoNotFoundCondition(err error) {
	rs.SetCondition(api.ResourceSyncAccessible, "accessible", "repository resource not found", err)
}

func (rs *ResourceSync) AddRepoAccessCondition(err error) {
	rs.SetCondition(api.ResourceSyncAccessible, "accessible", "failed to clone repository", err)
}

func (rs *ResourceSync) AddPathAccessCondition(err error) {
	rs.SetCondition(api.ResourceSyncAccessible, "accessible", "path not found in repository", err)
}

func (rs *ResourceSync) AddResourceParsedCondition(err error) {
	rs.SetCondition(api.ResourceSyncResourceParsed, "Success", "Fail", err)
}

func (rs *ResourceSync) AddSyncedCondition(err error) {
	rs.SetCondition(api.ResourceSyncSynced, "Success", "Fail", err)
}

func (rs *ResourceSync) GetKind() string {
	return api.ResourceSyncKind
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
