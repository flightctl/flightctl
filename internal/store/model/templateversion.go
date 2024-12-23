package model

import (
	"encoding/json"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

type TemplateVersion struct {
	OrgID           uuid.UUID      `gorm:"type:uuid;primary_key;"`
	Name            string         `gorm:"primary_key;" selector:"metadata.name"`
	FleetName       string         `gorm:"primary_key;" selector:"metadata.owner"`
	Fleet           Fleet          `gorm:"foreignkey:OrgID,FleetName;constraint:OnDelete:CASCADE;"`
	Labels          pq.StringArray `gorm:"type:text[]" selector:"metadata.labels"`
	Annotations     pq.StringArray `gorm:"type:text[]" selector:"metadata.annotations"`
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

type TemplateVersionList []TemplateVersion

func (t TemplateVersion) String() string {
	val, _ := json.Marshal(t)
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
		Annotations:     util.LabelMapToArray(resource.Metadata.Annotations),
		Spec:            MakeJSONField(resource.Spec),
		Status:          MakeJSONField(status),
		ResourceVersion: resourceVersion,
	}, nil
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

	status := api.TemplateVersionStatus{}
	if t.Status != nil {
		status = t.Status.Data
	}

	metadataAnnotations := util.LabelArrayToMap(t.Annotations)

	return api.TemplateVersion{
		ApiVersion: api.TemplateVersionAPIVersion,
		Kind:       api.TemplateVersionKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(t.Name),
			CreationTimestamp: util.TimeToPtr(t.CreatedAt.UTC()),
			Generation:        t.Generation,
			Owner:             util.SetResourceOwner(api.FleetKind, t.FleetName),
			Annotations:       &metadataAnnotations,
			ResourceVersion:   lo.Ternary(t.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(t.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}
}

func (tl TemplateVersionList) ToApiResource(cont *string, numRemaining *int64) api.TemplateVersionList {
	// Shouldn't happen, but just to be safe
	if tl == nil {
		return api.TemplateVersionList{
			ApiVersion: api.TemplateVersionAPIVersion,
			Kind:       api.TemplateVersionListKind,
		}
	}

	deviceList := make([]api.TemplateVersion, len(tl))
	for i, device := range tl {
		deviceList[i] = device.ToApiResource()
	}
	ret := api.TemplateVersionList{
		ApiVersion: api.TemplateVersionAPIVersion,
		Kind:       api.TemplateVersionListKind,
		Items:      deviceList,
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret
}
