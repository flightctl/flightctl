package model

import (
	"encoding/json"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
)

var (
	ResourceSyncAPI      = "v1alpha1"
	ResourceSyncKind     = "ResourceSync"
	ResourceSyncListKind = "ResourceSyncList"
)

type ResourceSync struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.ResourceSyncSpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.ResourceSyncStatus]
}

type ResourceSyncList []ResourceSync

func (r *ResourceSync) String() string {
	val, _ := json.Marshal(r)
	return string(val)
}

func NewResourceSyncFromApiResource(resource *api.ResourceSync) *ResourceSync {
	if resource == nil || resource.Metadata.Name == nil {
		return &ResourceSync{}
	}

	var status api.ResourceSyncStatus
	if resource.Status != nil {
		status = *resource.Status
	}
	return &ResourceSync{
		Resource: Resource{
			Name:   *resource.Metadata.Name,
			Labels: util.LabelMapToArray(resource.Metadata.Labels),
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}
}

func (r *ResourceSync) ToApiResource() api.ResourceSync {
	if r == nil {
		return api.ResourceSync{}
	}

	var spec api.ResourceSyncSpec
	if r.Spec != nil {
		spec = r.Spec.Data
	}

	var status api.ResourceSyncStatus
	if r.Status != nil {
		status = r.Status.Data
	}

	metadataLabels := util.LabelArrayToMap(r.Resource.Labels)

	return api.ResourceSync{
		ApiVersion: ResourceSyncAPI,
		Kind:       ResourceSyncKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(r.Name),
			CreationTimestamp: util.StrToPtr(r.CreatedAt.UTC().Format(time.RFC3339)),
			Labels:            &metadataLabels,
		},
		Spec:   spec,
		Status: &status,
	}
}

func (rl ResourceSyncList) ToApiResource(cont *string, numRemaining *int64) api.ResourceSyncList {
	if rl == nil {
		return api.ResourceSyncList{
			ApiVersion: ResourceSyncAPI,
			Kind:       ResourceSyncListKind,
			Items:      []api.ResourceSync{},
		}
	}

	resourceSyncList := make([]api.ResourceSync, len(rl))
	for i, resourceSync := range rl {
		resourceSyncList[i] = resourceSync.ToApiResource()
	}
	ret := api.ResourceSyncList{
		ApiVersion: ResourceSyncAPI,
		Kind:       ResourceSyncListKind,
		Items:      resourceSyncList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret
}
