package model

import "github.com/google/uuid"

// Generic represents any API resource that can be stored
type Generic interface {
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
	HasNilSpec() bool
	HasSameSpecAs(any) bool
	GetStatusAsJson() ([]byte, error)
}

// GenericList represents a list of any API resource that can be stored
type GenericList interface {
	Length() int
	GetItem(int) Generic
	RemoveLast()
}

var _ Generic = (*Device)(nil)
var _ Generic = (*Fleet)(nil)
var _ Generic = (*CertificateSigningRequest)(nil)
var _ Generic = (*EnrollmentRequest)(nil)
var _ Generic = (*Repository)(nil)
var _ Generic = (*ResourceSync)(nil)
var _ Generic = (*TemplateVersion)(nil)

var _ GenericList = (*DeviceList)(nil)
var _ GenericList = (*FleetList)(nil)
var _ GenericList = (*CertificateSigningRequestList)(nil)
var _ GenericList = (*EnrollmentRequestList)(nil)
var _ GenericList = (*RepositoryList)(nil)
var _ GenericList = (*ResourceSyncList)(nil)
var _ GenericList = (*TemplateVersionList)(nil)
