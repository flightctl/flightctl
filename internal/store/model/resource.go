package model

import (
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Resource struct {
	// Composite Primary Key: Unique within a tenant (OrgID, Name)

	// Uniquely identifies the tenant the resource belongs to.
	// Assigned by IAM. Immutable.
	OrgID uuid.UUID `gorm:"type:uuid;primaryKey;index:,composite:org_name,priority:1"`

	// Uniquely identifies the resource within a tenant and schema.
	// Depending on the schema (kind), assigned by the device management system or the crypto identity of the device (public key). Immutable.
	// This may become a URN later, so it's important API users treat this as an opaque handle.
	Name string `gorm:"primaryKey;index:,composite:org_name,priority:2" selector:"metadata.name"`

	// User-defined name, if non-null used in the UI as a more human-friendly alias to the resource ID.
	// DisplayName string

	// The "kind/name" of the resource owner of this resource.
	Owner *string `gorm:"index:owner_idx" selector:"metadata.owner"`

	// Labels associated with the resource, used for selecting and querying objects.
	// Labels are stored as a JSONB object, supporting flexible indexing and querying capabilities.
	Labels JSONMap[string, string] `gorm:"type:jsonb" selector:"metadata.labels,hidden,private"`

	// Annotations associated with the resource, used for storing additional metadata.
	// Similar to labels, annotations are stored as a JSONB object to support flexible indexing and querying.
	Annotations JSONMap[string, string] `gorm:"type:jsonb" selector:"metadata.annotations,hidden,private"`

	Generation      *int64
	ResourceVersion *int64
	CreatedAt       time.Time `selector:"metadata.creationTimestamp"`
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`
}

func (r *Resource) BeforeCreate(tx *gorm.DB) error {
	if len(r.Name) == 0 {
		r.Name = uuid.New().String()
	}
	return nil
}

type APIResourceOption func(*apiResourceOptions)

type apiResourceOptions struct {
	devicesSummary       *api.DevicesSummary // Used by Fleet
	isRendered           bool                // Used by Device
	knownRenderedVersion *string
}

func WithRendered(knownRenderedVersion *string) APIResourceOption {
	return func(o *apiResourceOptions) {
		o.isRendered = true
		o.knownRenderedVersion = knownRenderedVersion
	}
}

func WithDevicesSummary(devicesSummary *api.DevicesSummary) APIResourceOption {
	return func(o *apiResourceOptions) {
		o.devicesSummary = devicesSummary
	}
}

func (r *Resource) GetName() string {
	return r.Name
}

func (r *Resource) GetOrgID() uuid.UUID {
	return r.OrgID
}

func (r *Resource) SetOrgID(orgId uuid.UUID) {
	r.OrgID = orgId
}

func (r *Resource) GetResourceVersion() *int64 {
	return r.ResourceVersion
}

func (r *Resource) SetResourceVersion(version *int64) {
	r.ResourceVersion = version
}

func (r *Resource) GetGeneration() *int64 {
	return r.Generation
}

func (r *Resource) SetGeneration(generation *int64) {
	r.Generation = generation
}

func (r *Resource) GetOwner() *string {
	return r.Owner
}

func (r *Resource) SetOwner(owner *string) {
	r.Owner = owner
}

func (r *Resource) GetLabels() JSONMap[string, string] {
	return r.Labels
}

func (r *Resource) SetLabels(labels JSONMap[string, string]) {
	r.Labels = labels
}

func (r *Resource) GetAnnotations() JSONMap[string, string] {
	return r.Annotations
}

func (r *Resource) SetAnnotations(annotations JSONMap[string, string]) {
	r.Annotations = annotations
}

func (r *Resource) GetNonNilFieldsFromResource() []string {
	ret := []string{}
	if r.GetGeneration() != nil {
		ret = append(ret, "generation")
	}
	if r.GetLabels() != nil {
		ret = append(ret, "labels")
	}
	if r.GetOwner() != nil {
		ret = append(ret, "owner")
	}
	if r.GetAnnotations() != nil {
		ret = append(ret, "annotations")
	}
	if r.GetResourceVersion() != nil {
		ret = append(ret, "resource_version")
	}
	return ret
}

type ResourceInterface interface {
	GetKind() string
	GetName() string
	GetOrgID() uuid.UUID
	SetOrgID(uuid.UUID)
	GetResourceVersion() *int64
	SetResourceVersion(*int64)
	GetGeneration() *int64
	SetGeneration(*int64)
	GetOwner() *string
	SetOwner(*string)
	GetLabels() JSONMap[string, string]
	SetLabels(JSONMap[string, string])
	GetAnnotations() JSONMap[string, string]
	SetAnnotations(JSONMap[string, string])
	GetNonNilFieldsFromResource() []string
	HasNilSpec() bool
	HasSameSpecAs(any) bool
	GetStatusAsJson() ([]byte, error)
}

var _ ResourceInterface = (*Device)(nil)
var _ ResourceInterface = (*Fleet)(nil)
var _ ResourceInterface = (*CertificateSigningRequest)(nil)
var _ ResourceInterface = (*EnrollmentRequest)(nil)
var _ ResourceInterface = (*Repository)(nil)
var _ ResourceInterface = (*ResourceSync)(nil)
var _ ResourceInterface = (*TemplateVersion)(nil)
var _ ResourceInterface = (*Event)(nil)
