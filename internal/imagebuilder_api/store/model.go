package store

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

// Compile-time check that ImageBuild implements model.ResourceInterface
var _ model.ResourceInterface = (*ImageBuild)(nil)

// Compile-time check that ImageExport implements model.ResourceInterface
var _ model.ResourceInterface = (*ImageExport)(nil)

type ImageBuild struct {
	model.Resource

	// The desired state, stored as opaque JSON object.
	Spec *model.JSONField[domain.ImageBuildSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *model.JSONField[domain.ImageBuildStatus] `gorm:"type:jsonb"`

	// Logs contains the last 500 lines of build logs for completed builds.
	// This is separate from Status to keep the ImageBuild resource lightweight.
	// Logs are only accessible via the /log endpoint.
	Logs *string `gorm:"type:text"`
}

func (i ImageBuild) String() string {
	val, _ := json.Marshal(i)
	return string(val)
}

func NewImageBuildFromDomain(resource *domain.ImageBuild) (*ImageBuild, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &ImageBuild{}, nil
	}

	status := domain.ImageBuildStatus{}
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
	return &ImageBuild{
		Resource: model.Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			ResourceVersion: resourceVersion,
		},
		Spec:   model.MakeJSONField(resource.Spec),
		Status: model.MakeJSONField(status),
	}, nil
}

func ImageBuildAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.ImageBuildAPIVersion)
}

type ImageBuildDomainOption func(*imageBuildDomainOptions)

type imageBuildDomainOptions struct {
	imageExports []domain.ImageExport
}

func WithImageExports(imageExports []domain.ImageExport) ImageBuildDomainOption {
	return func(o *imageBuildDomainOptions) {
		o.imageExports = imageExports
	}
}

func (i *ImageBuild) ToDomain(opts ...ImageBuildDomainOption) (*domain.ImageBuild, error) {
	if i == nil {
		return &domain.ImageBuild{}, nil
	}

	options := imageBuildDomainOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	spec := domain.ImageBuildSpec{}
	if i.Spec != nil {
		spec = i.Spec.Data
	}

	status := domain.ImageBuildStatus{}
	if i.Status != nil {
		status = i.Status.Data
	}

	result := &domain.ImageBuild{
		ApiVersion: ImageBuildAPIVersion(),
		Kind:       string(domain.ResourceKindImageBuild),
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(i.Name),
			CreationTimestamp: lo.ToPtr(i.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(i.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(i.Resource.Annotations)),
			Generation:        i.Generation,
			Owner:             i.Owner,
			ResourceVersion:   lo.Ternary(i.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(i.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}

	// Add imageexports field if provided
	if len(options.imageExports) > 0 {
		result.Imageexports = &options.imageExports
	}

	return result, nil
}

func ImageBuildsToDomain(imageBuilds []ImageBuild, cont *string, numRemaining *int64) (domain.ImageBuildList, error) {
	return ImageBuildsToDomainWithOptions(imageBuilds, cont, numRemaining, nil)
}

func ImageBuildsToDomainWithOptions(imageBuilds []ImageBuild, cont *string, numRemaining *int64, imageExportsMap map[string][]domain.ImageExport) (domain.ImageBuildList, error) {
	imageBuildList := make([]domain.ImageBuild, len(imageBuilds))
	for i, imageBuild := range imageBuilds {
		var domainOpts []ImageBuildDomainOption
		if imageExportsMap != nil {
			if exports, ok := imageExportsMap[imageBuild.Name]; ok && len(exports) > 0 {
				domainOpts = append(domainOpts, WithImageExports(exports))
			}
		}
		domainResource, _ := imageBuild.ToDomain(domainOpts...)
		imageBuildList[i] = *domainResource
	}
	ret := domain.ImageBuildList{
		ApiVersion: ImageBuildAPIVersion(),
		Kind:       domain.ImageBuildListKind,
		Items:      imageBuildList,
		Metadata:   domain.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (i *ImageBuild) GetKind() string {
	return string(domain.ResourceKindImageBuild)
}

func (i *ImageBuild) HasNilSpec() bool {
	return i.Spec == nil
}

func (i *ImageBuild) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*ImageBuild)
	if !ok {
		return false
	}
	if other == nil {
		return false
	}
	if i.Spec == nil && other.Spec == nil {
		return true
	}
	if (i.Spec == nil && other.Spec != nil) || (i.Spec != nil && other.Spec == nil) {
		return false
	}
	// Compare specs by JSON marshaling
	thisSpec, _ := json.Marshal(i.Spec.Data)
	otherSpec, _ := json.Marshal(other.Spec.Data)
	return string(thisSpec) == string(otherSpec)
}

func (i *ImageBuild) GetStatusAsJson() ([]byte, error) {
	if i.Status == nil {
		return []byte("{}"), nil
	}
	return i.Status.MarshalJSON()
}

// ImageExport model
type ImageExport struct {
	model.Resource

	// The desired state, stored as opaque JSON object.
	Spec *model.JSONField[domain.ImageExportSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *model.JSONField[domain.ImageExportStatus] `gorm:"type:jsonb"`

	// Logs contains the last 500 lines of export logs for completed exports.
	// This is separate from Status to keep the ImageExport resource lightweight.
	// Logs are only accessible via the /log endpoint.
	Logs *string `gorm:"type:text"`
}

func (i ImageExport) String() string {
	val, _ := json.Marshal(i)
	return string(val)
}

func NewImageExportFromDomain(resource *domain.ImageExport) (*ImageExport, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &ImageExport{}, nil
	}

	status := domain.ImageExportStatus{}
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
	return &ImageExport{
		Resource: model.Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			ResourceVersion: resourceVersion,
		},
		Spec:   model.MakeJSONField(resource.Spec),
		Status: model.MakeJSONField(status),
	}, nil
}

func ImageExportAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.ImageExportAPIVersion)
}

func (i *ImageExport) ToDomain() (*domain.ImageExport, error) {
	if i == nil {
		return &domain.ImageExport{}, nil
	}

	spec := domain.ImageExportSpec{}
	if i.Spec != nil {
		spec = i.Spec.Data
	}

	status := domain.ImageExportStatus{}
	if i.Status != nil {
		status = i.Status.Data
	}

	return &domain.ImageExport{
		ApiVersion: ImageExportAPIVersion(),
		Kind:       string(domain.ResourceKindImageExport),
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(i.Name),
			CreationTimestamp: lo.ToPtr(i.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(i.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(i.Resource.Annotations)),
			Generation:        i.Generation,
			Owner:             i.Owner,
			ResourceVersion:   lo.Ternary(i.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(i.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func ImageExportsToDomain(imageExports []ImageExport, cont *string, numRemaining *int64) (domain.ImageExportList, error) {
	imageExportList := make([]domain.ImageExport, len(imageExports))
	for i, imageExport := range imageExports {
		domainResource, _ := imageExport.ToDomain()
		imageExportList[i] = *domainResource
	}
	ret := domain.ImageExportList{
		ApiVersion: ImageExportAPIVersion(),
		Kind:       domain.ImageExportListKind,
		Items:      imageExportList,
		Metadata:   domain.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (i *ImageExport) GetKind() string {
	return string(domain.ResourceKindImageExport)
}

func (i *ImageExport) HasNilSpec() bool {
	return i.Spec == nil
}

func (i *ImageExport) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*ImageExport)
	if !ok {
		return false
	}
	if other == nil {
		return false
	}
	if i.Spec == nil && other.Spec == nil {
		return true
	}
	if (i.Spec == nil && other.Spec != nil) || (i.Spec != nil && other.Spec == nil) {
		return false
	}
	// Compare specs by JSON marshaling
	thisSpec, _ := json.Marshal(i.Spec.Data)
	otherSpec, _ := json.Marshal(other.Spec.Data)
	return string(thisSpec) == string(otherSpec)
}

func (i *ImageExport) GetStatusAsJson() ([]byte, error) {
	if i.Status == nil {
		return []byte("{}"), nil
	}
	return i.Status.MarshalJSON()
}

// Field selector support for ImageBuild
var imageBuildStatusSelectors = map[selector.SelectorName]selector.SelectorType{
	selector.NewSelectorName("status.conditions.ready.reason"): selector.String,
	selector.NewSelectorName("status.lastSeen"):                selector.Timestamp,
}

// Field selector support for ImageExport
var imageExportSpecSelectors = map[selector.SelectorName]selector.SelectorType{
	selector.NewSelectorName("spec.source.imageBuildRef"): selector.String,
}

var imageExportStatusSelectors = map[selector.SelectorName]selector.SelectorType{
	selector.NewSelectorName("status.conditions.ready.reason"): selector.String,
	selector.NewSelectorName("status.lastSeen"):                selector.Timestamp,
}

// ResolveSelector resolves a field selector name to a SelectorField for ImageBuild
func (i *ImageBuild) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := imageBuildStatusSelectors[name]; exists {
		return makeImageBuildStatusJSONBSelectorField(name, typ)
	}
	return nil, fmt.Errorf("unable to resolve selector for image build")
}

// ListSelectors returns all available field selectors for ImageBuild
func (i *ImageBuild) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(imageBuildStatusSelectors))
	for sn := range imageBuildStatusSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(keys...)
}

// ResolveSelector resolves a field selector name to a SelectorField for ImageExport
func (i *ImageExport) ResolveSelector(name selector.SelectorName) (*selector.SelectorField, error) {
	if typ, exists := imageExportSpecSelectors[name]; exists {
		return makeImageExportJSONBSelectorField(name, typ)
	}
	if typ, exists := imageExportStatusSelectors[name]; exists {
		return makeImageExportStatusJSONBSelectorField(name, typ)
	}
	return nil, fmt.Errorf("unable to resolve selector for image export")
}

// ListSelectors returns all available field selectors for ImageExport
func (i *ImageExport) ListSelectors() selector.SelectorNameSet {
	keys := make([]selector.SelectorName, 0, len(imageExportSpecSelectors)+len(imageExportStatusSelectors))
	for sn := range imageExportSpecSelectors {
		keys = append(keys, sn)
	}
	for sn := range imageExportStatusSelectors {
		keys = append(keys, sn)
	}
	return selector.NewSelectorFieldNameSet().Add(keys...)
}

// makeImageExportJSONBSelectorField creates a SelectorField for JSONB fields in ImageExport
func makeImageExportJSONBSelectorField(selectorName selector.SelectorName, selectorType selector.SelectorType) (*selector.SelectorField, error) {
	selectorStr := selectorName.String()
	if len(selectorStr) == 0 {
		return nil, fmt.Errorf("jsonb selector name cannot be empty")
	}

	var params strings.Builder
	parts := strings.Split(selectorStr, ".")
	params.WriteString(parts[0])

	lastIndex := len(parts[1:]) - 1
	for i, part := range parts[1:] {
		if i == lastIndex && selectorType != selector.Jsonb {
			params.WriteString(" ->> '")
		} else {
			params.WriteString(" -> '")
		}
		params.WriteString(part)
		params.WriteString("'")
	}

	return &selector.SelectorField{
		Name:      selectorName,
		Type:      selectorType,
		FieldName: params.String(),
		FieldType: "jsonb",
	}, nil
}

// makeImageBuildStatusJSONBSelectorField creates a SelectorField for status condition fields in ImageBuild
// Handles status.conditions.ready.reason by querying the JSONB array
func makeImageBuildStatusJSONBSelectorField(selectorName selector.SelectorName, selectorType selector.SelectorType) (*selector.SelectorField, error) {
	selectorStr := selectorName.String()
	switch selectorStr {
	case "status.conditions.ready.reason":
		// Query JSONB array to find condition with type="Ready" and extract its reason
		// This uses a subquery to find the condition in the array
		return &selector.SelectorField{
			Name:      selectorName,
			Type:      selectorType,
			FieldName: `(SELECT elem->>'reason' FROM jsonb_array_elements(COALESCE(status, '{}'::jsonb)->'conditions') AS elem WHERE elem->>'type' = 'Ready' LIMIT 1)`,
			FieldType: "jsonb",
		}, nil
	case "status.lastSeen":
		// Extract lastSeen timestamp from status JSONB
		return &selector.SelectorField{
			Name:      selectorName,
			Type:      selectorType,
			FieldName: `(status->>'lastSeen')::timestamp`,
			FieldType: "jsonb",
		}, nil
	}
	return nil, fmt.Errorf("unsupported status selector: %s", selectorStr)
}

// makeImageExportStatusJSONBSelectorField creates a SelectorField for status condition fields in ImageExport
// Handles status.conditions.ready.reason by querying the JSONB array
func makeImageExportStatusJSONBSelectorField(selectorName selector.SelectorName, selectorType selector.SelectorType) (*selector.SelectorField, error) {
	selectorStr := selectorName.String()
	switch selectorStr {
	case "status.conditions.ready.reason":
		// Query JSONB array to find condition with type="Ready" and extract its reason
		// This uses a subquery to find the condition in the array
		return &selector.SelectorField{
			Name:      selectorName,
			Type:      selectorType,
			FieldName: `(SELECT elem->>'reason' FROM jsonb_array_elements(COALESCE(status, '{}'::jsonb)->'conditions') AS elem WHERE elem->>'type' = 'Ready' LIMIT 1)`,
			FieldType: "jsonb",
		}, nil
	case "status.lastSeen":
		// Extract lastSeen timestamp from status JSONB
		return &selector.SelectorField{
			Name:      selectorName,
			Type:      selectorType,
			FieldName: `(status->>'lastSeen')::timestamp`,
			FieldType: "jsonb",
		}, nil
	}
	return nil, fmt.Errorf("unsupported status selector: %s", selectorStr)
}
