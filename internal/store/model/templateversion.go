package model

import (
	"encoding/json"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
)

var (
	TemplateVersionAPI      = "v1alpha1"
	TemplateVersionKind     = "TemplateVersion"
	TemplateVersionListKind = "TemplateVersionList"
)

type TemplateVersion struct {
	ResourceWithPrimaryKeyOwner

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.TemplateVersionSpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.TemplateVersionStatus]

	// An indication if this version is valid. It exposed in a Condition but easier to query here.
	Valid *bool
}

type TemplateVersionList []TemplateVersion

func (t TemplateVersion) String() string {
	val, _ := json.Marshal(t)
	return string(val)
}

func NewTemplateVersionFromApiResource(resource *api.TemplateVersion) *TemplateVersion {
	// Shouldn't happen, but just to be safe
	if resource == nil || resource.Metadata.Name == nil {
		return &TemplateVersion{}
	}

	var status api.TemplateVersionStatus
	if resource.Status != nil {
		status = *resource.Status
	}

	return &TemplateVersion{
		ResourceWithPrimaryKeyOwner: ResourceWithPrimaryKeyOwner{
			Name:        *resource.Metadata.Name,
			Generation:  resource.Metadata.Generation,
			Owner:       resource.Metadata.Owner,
			Annotations: util.LabelMapToArray(resource.Metadata.Annotations),
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}
}

func (t *TemplateVersion) ToApiResource() api.TemplateVersion {
	// Shouldn't happen, but just to be safe
	if t == nil {
		return api.TemplateVersion{}
	}

	var spec api.TemplateVersionSpec
	if t.Spec != nil {
		spec = t.Spec.Data
	}

	var status api.TemplateVersionStatus
	if t.Status != nil {
		status = t.Status.Data
	}

	metadataAnnotations := util.LabelArrayToMap(t.ResourceWithPrimaryKeyOwner.Annotations)

	return api.TemplateVersion{
		ApiVersion: TemplateVersionAPI,
		Kind:       TemplateVersionKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(t.Name),
			CreationTimestamp: util.StrToPtr(t.CreatedAt.UTC().Format(time.RFC3339)),
			Generation:        t.Generation,
			Owner:             t.Owner,
			Annotations:       &metadataAnnotations,
			ResourceVersion:   GetResourceVersion(t.UpdatedAt),
		},
		Spec:   spec,
		Status: &status,
	}
}

func (tl TemplateVersionList) ToApiResource(cont *string, numRemaining *int64) api.TemplateVersionList {
	// Shouldn't happen, but just to be safe
	if tl == nil {
		return api.TemplateVersionList{
			ApiVersion: TemplateVersionAPI,
			Kind:       TemplateVersionListKind,
		}
	}

	deviceList := make([]api.TemplateVersion, len(tl))
	for i, device := range tl {
		deviceList[i] = device.ToApiResource()
	}
	ret := api.TemplateVersionList{
		ApiVersion: TemplateVersionAPI,
		Kind:       TemplateVersionListKind,
		Items:      deviceList,
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret
}
