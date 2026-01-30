package v1beta1

// Converter aggregates all resource-specific converters for v1beta1 API.
type Converter interface {
	Device() DeviceConverter
	Fleet() FleetConverter
	Repository() RepositoryConverter
	EnrollmentRequest() EnrollmentRequestConverter
	CertificateSigningRequest() CertificateSigningRequestConverter
	AuthProvider() AuthProviderConverter
	ResourceSync() ResourceSyncConverter
	Catalog() CatalogConverter
	TemplateVersion() TemplateVersionConverter
	Event() EventConverter
	Organization() OrganizationConverter
	Common() CommonConverter
	Auth() AuthConverter
}

type converterImpl struct {
	device                    DeviceConverter
	fleet                     FleetConverter
	repository                RepositoryConverter
	enrollmentRequest         EnrollmentRequestConverter
	certificateSigningRequest CertificateSigningRequestConverter
	authProvider              AuthProviderConverter
	resourceSync              ResourceSyncConverter
	catalog                   CatalogConverter
	templateVersion           TemplateVersionConverter
	event                     EventConverter
	organization              OrganizationConverter
	common                    CommonConverter
	auth                      AuthConverter
}

// NewConverter creates a new Converter instance with all resource converters.
func NewConverter() Converter {
	return &converterImpl{
		device:                    NewDeviceConverter(),
		fleet:                     NewFleetConverter(),
		repository:                NewRepositoryConverter(),
		enrollmentRequest:         NewEnrollmentRequestConverter(),
		certificateSigningRequest: NewCertificateSigningRequestConverter(),
		authProvider:              NewAuthProviderConverter(),
		resourceSync:              NewResourceSyncConverter(),
		catalog:                   NewCatalogConverter(),
		templateVersion:           NewTemplateVersionConverter(),
		event:                     NewEventConverter(),
		organization:              NewOrganizationConverter(),
		common:                    NewCommonConverter(),
		auth:                      NewAuthConverter(),
	}
}

func (c *converterImpl) Device() DeviceConverter {
	return c.device
}

func (c *converterImpl) Fleet() FleetConverter {
	return c.fleet
}

func (c *converterImpl) Repository() RepositoryConverter {
	return c.repository
}

func (c *converterImpl) EnrollmentRequest() EnrollmentRequestConverter {
	return c.enrollmentRequest
}

func (c *converterImpl) CertificateSigningRequest() CertificateSigningRequestConverter {
	return c.certificateSigningRequest
}

func (c *converterImpl) AuthProvider() AuthProviderConverter {
	return c.authProvider
}

func (c *converterImpl) ResourceSync() ResourceSyncConverter {
	return c.resourceSync
}

func (c *converterImpl) Catalog() CatalogConverter {
	return c.catalog
}

func (c *converterImpl) TemplateVersion() TemplateVersionConverter {
	return c.templateVersion
}

func (c *converterImpl) Event() EventConverter {
	return c.event
}

func (c *converterImpl) Organization() OrganizationConverter {
	return c.organization
}

func (c *converterImpl) Common() CommonConverter {
	return c.common
}

func (c *converterImpl) Auth() AuthConverter {
	return c.auth
}
