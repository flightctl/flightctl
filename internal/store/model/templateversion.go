package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

type TemplateVersion struct {
	OrgID           uuid.UUID               `gorm:"type:uuid;primary_key;"`
	Name            string                  `gorm:"primary_key;" selector:"metadata.name"`
	FleetName       string                  `gorm:"primary_key;" selector:"metadata.owner"`
	Fleet           Fleet                   `gorm:"foreignkey:OrgID,FleetName;constraint:OnDelete:CASCADE;"`
	Labels          JSONMap[string, string] `gorm:"type:jsonb" selector:"metadata.labels,hidden,private"`
	Annotations     JSONMap[string, string] `gorm:"type:jsonb" selector:"metadata.annotations,hidden,private"`
	Generation      *int64
	ResourceVersion *int64
	CreatedAt       time.Time `selector:"metadata.creationTimestamp"`
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.TemplateVersionSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.TemplateVersionStatus] `gorm:"type:jsonb"`
}

func (tv TemplateVersion) String() string {
	val, _ := json.Marshal(tv)
	return string(val)
}

func NewTemplateVersionFromApiResource(resource *api.TemplateVersion) (*TemplateVersion, error) {
	// Shouldn't happen, but just to be safe
	if resource == nil || resource.Metadata.Name == nil {
		return &TemplateVersion{}, nil
	}

	status := api.TemplateVersionStatus{}
	if resource.Status != nil {
		status = *resource.Status
	}

	_, ownerName, _ := util.GetResourceOwner(resource.Metadata.Owner)
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}
	return &TemplateVersion{
		Name:            *resource.Metadata.Name,
		Generation:      resource.Metadata.Generation,
		FleetName:       ownerName,
		Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
		Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
		Spec:            MakeJSONField(resource.Spec),
		Status:          MakeJSONField(status),
		ResourceVersion: resourceVersion,
	}, nil
}

func TemplateVersionAPIVersion() string {
	return fmt.Sprintf("%s/%s", api.APIGroup, api.TemplateVersionAPIVersion)
}

func (tv *TemplateVersion) ToApiResource(opts ...APIResourceOption) (*api.TemplateVersion, error) {
	// Shouldn't happen, but just to be safe
	if tv == nil {
		return &api.TemplateVersion{}, nil
	}

	var spec api.TemplateVersionSpec
	if tv.Spec != nil {
		spec = tv.Spec.Data
	}

	status := api.TemplateVersionStatus{}
	if tv.Status != nil {
		status = tv.Status.Data
	}

	return &api.TemplateVersion{
		ApiVersion: TemplateVersionAPIVersion(),
		Kind:       api.TemplateVersionKind,
		Metadata: api.ObjectMeta{
			Name:              lo.ToPtr(tv.Name),
			CreationTimestamp: lo.ToPtr(tv.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(tv.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(tv.Annotations)),
			Generation:        tv.Generation,
			Owner:             util.SetResourceOwner(api.FleetKind, tv.FleetName),
			ResourceVersion:   lo.Ternary(tv.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(tv.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func TemplateVersionsToApiResource(tvs []TemplateVersion, cont *string, numRemaining *int64) (api.TemplateVersionList, error) {
	deviceList := make([]api.TemplateVersion, len(tvs))
	for i, device := range tvs {
		apiResource, _ := device.ToApiResource()
		deviceList[i] = *apiResource
	}
	ret := api.TemplateVersionList{
		ApiVersion: TemplateVersionAPIVersion(),
		Kind:       api.TemplateVersionListKind,
		Items:      deviceList,
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (tv *TemplateVersion) GetKind() string {
	return api.TemplateVersionKind
}

func (tv *TemplateVersion) GetName() string {
	return tv.Name
}

func (tv *TemplateVersion) GetOrgID() uuid.UUID {
	return tv.OrgID
}

func (tv *TemplateVersion) SetOrgID(orgId uuid.UUID) {
	tv.OrgID = orgId
}

func (tv *TemplateVersion) GetResourceVersion() *int64 {
	return tv.ResourceVersion
}

func (tv *TemplateVersion) SetResourceVersion(version *int64) {
	tv.ResourceVersion = version
}

func (tv *TemplateVersion) GetGeneration() *int64 {
	return tv.Generation
}

func (tv *TemplateVersion) SetGeneration(generation *int64) {
	tv.Generation = generation
}

func (tv *TemplateVersion) GetOwner() *string {
	return nil
}

func (tv *TemplateVersion) SetOwner(owner *string) {}

func (tv *TemplateVersion) GetLabels() JSONMap[string, string] {
	return tv.Labels
}

func (tv *TemplateVersion) SetLabels(labels JSONMap[string, string]) {
	tv.Labels = labels
}

func (tv *TemplateVersion) GetAnnotations() JSONMap[string, string] {
	return tv.Annotations
}

func (tv *TemplateVersion) SetAnnotations(annotations JSONMap[string, string]) {
	tv.Annotations = annotations
}

func (tv *TemplateVersion) GetNonNilFieldsFromResource() []string {
	ret := []string{}
	if tv.GetGeneration() != nil {
		ret = append(ret, "generation")
	}
	if tv.GetLabels() != nil {
		ret = append(ret, "labels")
	}
	if tv.GetAnnotations() != nil {
		ret = append(ret, "annotations")
	}
	if tv.GetResourceVersion() != nil {
		ret = append(ret, "resource_version")
	}
	return ret
}

func (tv *TemplateVersion) HasNilSpec() bool {
	return tv.Spec == nil
}

func (tv *TemplateVersion) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*TemplateVersion) // Assert that the other resource is a *TemplateVersion
	if !ok {
		return false // Not the same type, so specs cannot be the same
	}
	if other == nil {
		return false
	}
	if tv.Spec == nil && other.Spec == nil {
		return true
	}
	if (tv.Spec == nil && other.Spec != nil) || (tv.Spec != nil && other.Spec == nil) {
		return false
	}
	return reflect.DeepEqual(tv.Spec.Data, other.Spec.Data)
}

func (tv *TemplateVersion) GetStatusAsJson() ([]byte, error) {
	if tv.Status == nil {
		return []byte("{}"), nil
	}
	return tv.Status.MarshalJSON()
}

func (tv *TemplateVersion) GetTimestamp() time.Time {
	return tv.CreatedAt
}
